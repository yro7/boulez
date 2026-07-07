package daemon

import (
	"fmt"
	"github.com/yro7/boulez/config"
	"github.com/yro7/boulez/host"
	"github.com/yro7/boulez/kernel"
	"github.com/yro7/boulez/log"
	"github.com/yro7/boulez/program"
	"github.com/yro7/boulez/protected"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// RunDaemon runs the daemon process which iterates over all sessions and runs AutoYes mode on them.
// It's expected that the main process kills the daemon when the main process starts.
//
// INVARIANT: at most one RunDaemon is alive per config dir (#daemon ∈ {0,1}).
// The lock is held for the daemon's entire lifetime — released by the OS
// automatically on exit (even crash). This makes `boulez daemon run` safe
// to invoke by any caller (auto-launch, manual, future OS service): a second
// invocation exits cleanly without double-binding the socket.
func RunDaemon(cfg *config.Config) error {
	// Acquire the singleton lock BEFORE touching the socket or starting any
	// goroutine. This is the invariant gate: the previous design only locked
	// in LaunchDaemon (the auto-launch path), so `boulez daemon run` invoked
	// manually or by a future OS service bypassed it entirely and N daemons
	// could run at once (a real bug seen in dogfooding). Holding an flock for
	// the whole lifetime means a crashed daemon releases it automatically —
	// no stale-PID heuristic to get wrong.
	lockPath, err := daemonLockPath()
	if err != nil {
		return fmt.Errorf("failed to resolve daemon lock path: %w", err)
	}
	release, err := acquireDaemonLock(lockPath)
	if err != nil {
		return err // already a daemon alive (or lock unusable) — refuse cleanly
	}
	defer release()

	log.InfoLog.Printf("starting daemon")
	state := config.LoadState()
	storage := kernel.NewStorage(state)

	// AutoYes is per-instance (persisted on InstanceData). The daemon respects
	// the stored value rather than forcing it globally — this lets remote
	// instances stay off by default while local ones follow the user's choice.

	// The kernel is the single writer that owns the fleet's mutable state. The
	// daemon does NOT keep its own instances slice: doing so creates a second
	// writer that drifts from the kernel (it never sees the orchestrator the
	// kernel spawns via Ensure), and the daemon's shutdown-save would then
	// clobber the kernel's persisted state — losing the orchestrator. Instead
	// the poll loop below reads k.LiveInstances() (the kernel's source of
	// truth) every tick. The kernel persists every mutation itself (autosave).

	// The kernel is the single-writer control authority. The daemon owns it
	// and serves the control socket so `boulez ctl` (and future LLM tools) can
	// drive the fleet. The auto-yes loop below runs alongside.
	//
	// Protected branches (spec decision 7): the daemon has no cwd and no repo
	// of its own (it is a service, C2.2), so it cannot derive "the branch the
	// user is standing on." Instead, protected branches are declared
	// explicitly per repo in the protected store (~/.boulez/protected.json) and
	// fed to the kernel as a flat, kernel-wide guard. The store is reloaded
	// on SIGHUP (see reloadProtected below) without reconstructing the kernel.
	// The conventional main/master guard in the Merger remains as defense in
	// depth.
	protectedStore, err := protected.New()
	if err != nil {
		return fmt.Errorf("failed to open protected store: %w", err)
	}
	protected, _ := protectedStore.Flat()
	k := kernel.New(storage, kernel.WithSpawner(kernelSpawner{}), kernel.WithMerger(realMerger{}), kernel.WithProtectedBranches(protected))

	// NOTE: the daemon no longer auto-spawns a global orchestrator. The old
	// always-on "instance 0" bootstrap (orchestrator.Ensure at startup + a
	// periodic EnsureLive probe) never worked reliably and is gone. An
	// orchestrator is now spawned explicitly by the user from the TUI via the
	// O key (app.spawnOrchestrator) — same as any other instance. The daemon
	// still owns the kernel/control socket so `boulez ctl` works; it just no
	// longer has any orchestrator-specific policy.

	socketPath, err := kernel.SocketPath()
	if err != nil {
		return fmt.Errorf("failed to resolve kernel socket path: %w", err)
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	stopCh := make(chan struct{})

	// Bind and serve the control socket FIRST — before any boot work that can
	// block on a remote host. If Serve fails to bind, that is fatal: a daemon
	// that cannot serve is useless AND harmful, since it holds the singleton
	// lock + pid and wedges every client and every relaunch. serveErr signals
	// the shutdown loop below so the process exits and releases its locks.
	serveErr := make(chan error, 1)
	go func() {
		defer wg.Done()
		if err := kernel.Serve(k, socketPath); err != nil {
			log.ErrorLog.Printf("kernel serve failed: %v", err)
			serveErr <- err
		}
	}()

	// C4.4: after the socket is serving, demote any instance whose tmux session
	// is gone (e.g. a daemon restart following a tmux crash) from Running to
	// Dead, so the user sees a dead instance instead of a ghost "running". Only
	// a definitive "session absent" from tmux demotes — never a timeout.
	//
	// Run in the BACKGROUND: for a remote instance this shells out to
	// `ssh <alias> tmux has-session`, which — even bounded by BatchMode +
	// ConnectTimeout — can take seconds against an unreachable host. It ran
	// synchronously on the boot path before the socket was bound, so a stalled
	// remote probe left the daemon alive-but-not-serving and wedged the whole
	// system. It must never gate the serve loop.
	go k.ReconcileLiveness()

	pollInterval := time.Duration(cfg.DaemonPollInterval) * time.Millisecond
	// If we get an error for a session, it's likely that we'll keep getting the error. Log every 30 seconds.
	everyN := log.NewEvery(60 * time.Second)

	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTimer(pollInterval)
		for {
			for _, instance := range k.LiveInstances() {
				// We only store started instances, but check anyway.
				if instance.Started() && !instance.Paused() {
					if _, status, _ := instance.HasUpdated(); status == program.StatusReady || status == program.StatusPermission {
						// Only resolve prompts the agent's adapter knows how to dismiss
						// (permissions/trust). A bare "ready" prompt (agent waiting for free
						// user input) is NOT auto-dismissed: tapping Enter there would
						// send an empty input to the agent. Agent-specific knowledge of
						// what is resolvable lives in program.Adapter.
						instance.CheckAndHandleTrustPrompt()
						if err := instance.UpdateDiffStats(); err != nil {
							if everyN.ShouldLog() {
								log.WarningLog.Printf("could not update diff stats for %s: %v", instance.Title, err)
							}
						}
					}
				}
			}

			// Handle stop before ticker.
			select {
			case <-stopCh:
				return
			default:
			}

			<-ticker.C
			ticker.Reset(pollInterval)
		}
	}()

	// Notify on SIGINT (Ctrl+C) and SIGTERM for shutdown, and SIGHUP for
	// reload (C2.2: reload the protected-branch set without restarting the
	// daemon — the kernel's single-writer contract is preserved, only the
	// protected set is hot-swapped via SetProtectedBranches).
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	var fatalErr error
	for {
		select {
		case sig := <-sigChan:
			if sig == syscall.SIGHUP {
				reloadProtected(protectedStore, k)
				continue
			}
			log.InfoLog.Printf("received signal %s", sig.String())
		case err := <-serveErr:
			// The control socket could not be served. Exit so the singleton
			// lock and pid release and a fresh daemon can take over — never
			// linger alive-but-not-serving (that is the wedge we are avoiding).
			log.ErrorLog.Printf("daemon shutting down: control socket unavailable: %v", err)
			fatalErr = fmt.Errorf("kernel serve failed: %w", err)
		}
		break
	}

	// Stop the goroutine so we don't race.
	close(stopCh)
	wg.Wait()

	// Tear down every live SSH ControlMaster so they don't outlive the daemon.
	// Each Stop is best-effort (a missing master is not an error). Without this,
	// masters started by instance Starts would linger as orphan ssh processes
	// holding the alias's LocalForwards bound.
	host.StopAllMasters()

	// Note: the daemon no longer saves instances on shutdown. The kernel is
	// the single writer and persists every mutation as it happens (autosave).
	// A shutdown save here would race the kernel and risk clobbering state it
	// does not know about (the original double-writer bug that orphaned the
	// orchestrator on every restart).
	return fatalErr
}

// LaunchDaemon launches the daemon process. It is best-effort deduplication
// for the auto-launch path (a storm of concurrent `boulez ctl` calls with the
// daemon down). The HARD invariant is enforced inside RunDaemon via an flock
// held for the daemon's lifetime: even if multiple LaunchDaemon callers race
// and each spawns a child, only the first child that takes the lock survives —
// the rest exit cleanly at startup. So this function does not need to be
// perfect; it just avoids wasteful double-spawns.
func LaunchDaemon() error {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get config directory: %w", err)
	}

	lockPath := filepath.Join(configDir, "daemon.lock")
	acquired, err := acquireLaunchLock(lockPath)
	if err != nil {
		return fmt.Errorf("acquire launch lock: %w", err)
	}
	if !acquired {
		// Another launcher is starting (or a daemon is up). Let it.
		log.InfoLog.Printf("daemon launch already in progress; not launching a second")
		return nil
	}

	// Find the boulez binary.
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// `boulez daemon run` is the canonical daemon entrypoint (decision D2): the
	// OS service (Phase 2) and the auto-start both invoke it.
	cmd := exec.Command(execPath, "daemon", "run")

	// Detach the process from the parent
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	// Set process group to prevent signals from propagating
	cmd.SysProcAttr = getSysProcAttr()

	if err := cmd.Start(); err != nil {
		_ = os.Remove(lockPath)
		return fmt.Errorf("failed to start child process: %w", err)
	}

	log.InfoLog.Printf("started daemon child process with PID: %d", cmd.Process.Pid)

	// Save PID to a file for later management (StopDaemon consumes this). The
	// launch lock is a short-lived "launcher in flight" marker; the daemon
	// itself re-acquires the same path as an flock in RunDaemon and holds it
	// for its lifetime — that flock (not this file) is the real invariant.
	pidFile := filepath.Join(configDir, "daemon.pid")
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0644); err != nil {
		return fmt.Errorf("failed to write PID file: %w", err)
	}

	// Don't wait for the child to exit, it's detached
	return nil
}

// StopDaemon attempts to stop a running daemon process if it exists. Returns no error if the daemon is not found
// (assumes the daemon does not exist).
func StopDaemon() error {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get config directory: %w", err)
	}

	pidFile := filepath.Join(configDir, "daemon.pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read PID file: %w", err)
	}

	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		return fmt.Errorf("invalid PID file format: %w", err)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find daemon process: %w", err)
	}

	// Stop the daemon GRACEFULLY: SIGTERM lets RunDaemon's signal handler run
	// its shutdown path (close goroutines, and crucially host.StopAllMasters so
	// SSH ControlMasters don't outlive the daemon). SIGKILL (proc.Kill) would
	// bypass that handler entirely, leaking masters and the control socket.
	// If the process doesn't exit within a few seconds (wedged), escalate to
	// SIGKILL so `daemon stop` still reliably terminates it.
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		// Fall back to a hard kill if signaling failed (e.g. permission).
		if err := proc.Kill(); err != nil {
			return fmt.Errorf("failed to stop daemon process: %w", err)
		}
	}
	waitForProcessExit(proc, 5*time.Second)

	// Clean up the PID file. The flock inside RunDaemon is released
	// automatically when the killed process exits (OS-level), so we do NOT
	// touch daemon.lock here — removing it would defeat the launch-lock's
	// "launcher in flight" marker if a relaunch races in.
	if err := os.Remove(pidFile); err != nil {
		return fmt.Errorf("failed to remove PID file: %w", err)
	}

	log.InfoLog.Printf("daemon process (PID: %d) stopped successfully", pid)
	return nil
}

// reloadProtected re-reads the protected-branch store and pushes the new
// union into the running kernel (C2.2 SIGHUP contract). It is the daemon's
// hot-reload path: the kernel is never reconstructed (the single-writer
// invariant holds), only its protected set is swapped. Failures log and
// leave the previous set in place — a bad store must not empty protection
// (fail closed).
func reloadProtected(store *protected.Store, k *kernel.Kernel) {
	branches, err := store.Flat()
	if err != nil {
		log.WarningLog.Printf("SIGHUP: could not reload protected store (keeping previous set): %v", err)
		return
	}
	k.SetProtectedBranches(branches)
	log.InfoLog.Printf("SIGHUP: reloaded protected set (%d branch(es))", len(branches))
}

// acquireLaunchLock tries to atomically create the launch lock file. Returns
// true if THIS caller acquired it (and so owns the launch), false if another
// launcher already holds it. A stale lock (holder PID dead) is reclaimed.
//
// The lock is released implicitly: the daemon, once up, takes over and writes
// daemon.pid; the lock file itself is left in place as a "a daemon exists"
// sentinel. StopDaemon removes daemon.pid AND the lock when it kills the
// daemon. We do NOT release the lock on the launcher's exit (the launcher is
// short-lived; the lock outlives it as the "daemon is up" marker).
//
// NOTE: this is BEST-EFFORT deduplication for the auto-launch path only. The
// hard singleton invariant lives in RunDaemon via acquireDaemonLock (an flock
// held for the daemon's lifetime). Even if two LaunchDaemon callers both win
// here (rare) and each spawn a child, only the first child to acquire the
// daemon flock survives; the other exits cleanly at startup.
func acquireLaunchLock(path string) (bool, error) {
	// If a lock file exists, check whether its holder is alive.
	if data, err := os.ReadFile(path); err == nil {
		pidStr := strings.TrimSpace(string(data))
		if pid, perr := parsePID(pidStr); perr == nil {
			if !pidAlive(pid) {
				// Stale lock from a crashed launcher — reclaim it.
				_ = os.Remove(path)
			} else {
				// A launcher is mid-flight (or a daemon is up). Don't launch again.
				return false, nil
			}
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("read lock file: %w", err)
	}

	// Atomically create the lock with OUR pid. O_EXCL guarantees only one
	// concurrent writer wins across processes.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		if os.IsExist(err) {
			// Raced with another launcher that won — let it.
			return false, nil
		}
		return false, err
	}
	_, err = fmt.Fprintf(f, "%d", os.Getpid())
	_ = f.Close()
	if err != nil {
		_ = os.Remove(path)
		return false, err
	}
	return true, nil
}

// daemonLockPath returns the path to the singleton daemon lock file inside
// the boulez config dir. Same path is used by acquireLaunchLock (best-effort
// auto-launch dedup) and acquireDaemonLock (the hard invariant inside
// RunDaemon) so both layers agree on a single file.
func daemonLockPath() (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", fmt.Errorf("failed to get config directory: %w", err)
	}
	return filepath.Join(configDir, "daemon.lock"), nil
}

// acquireDaemonLock takes an exclusive flock on the daemon lock file and keeps
// it held for the daemon's lifetime. This is the HARD singleton invariant
// (#daemon ∈ {0,1}): regardless of how `boulez daemon run` was invoked
// (auto-launch, manual, future OS service), only ONE process can hold this
// lock at a time. A second invocation blocks on the flock until the holder
// exits — but since the holder is the daemon (long-lived), we don't want to
// block: we want to refuse. So we use LOCK_EX|LOCK_NB (non-blocking): if the
// lock is already held, we return an error immediately.
//
// Why flock over O_EXCL+PID: the flock is released automatically by the OS
// when the process exits — including crashes (SIGKILL, segfault) — so there
// is no stale-PID heuristic to get wrong. The lock file itself may be left
// on disk; that's harmless (it's just a path to lock, its existence doesn't
// mean "locked").
//
// The returned release func MUST be called on daemon shutdown (typically via
// defer) to release the lock cleanly; the OS would release it on exit anyway,
// but explicit release is good hygiene and makes shutdown deterministic.
func acquireDaemonLock(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock file %s: %w", path, err)
	}
	// LOCK_EX|LOCK_NB: exclusive, non-blocking. If another daemon holds the
	// lock, fail immediately instead of blocking forever (the holder is a
	// long-lived daemon, so blocking would hang the second invocation).
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another daemon is already running (lock %s held): %w", path, err)
	}
	// Write our PID into the (locked) file for diagnostics and StopDaemon.
	// Truncate first so a stale long PID from a previous run doesn't linger.
	if _, err := f.Seek(0, 0); err == nil {
		_ = f.Truncate(0)
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// waitForProcessExit blocks until proc is gone or timeout elapses, after
// which the caller escalates (e.g. SIGKILL). Used by StopDaemon so a
// graceful SIGTERM gets a bounded window to run shutdown (StopAllMasters,
// socket close) before the caller gives up on it.
func waitForProcessExit(proc *os.Process, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if proc.Signal(syscall.Signal(0)) != nil {
			return // process is gone
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// parsePID parses a decimal PID from a string (the lock/pid file contents).
func parsePID(s string) (int, error) {
	var pid int
	_, err := fmt.Sscanf(s, "%d", &pid)
	return pid, err
}

// pidAlive reports whether a process with the given PID exists. On any error
// it returns true (treat the holder as alive to avoid double-launching).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// findProcess always succeeds on Unix; the signal-zero probe is the real
	// liveness check (signal 0 = "is there a process?", no actual signal sent).
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

// WaitForSocket polls for the control socket to appear, up to timeout. Used
// by the ctl client after LaunchDaemon: instead of a blind sleep, wait
// actively (50ms cadence) for the daemon to bind. Returns nil if the socket
// appeared, an error on timeout.
func WaitForSocket(socketPath string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon socket %s did not appear within %s", socketPath, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
