package daemon

import (
	"claude-squad/config"
	"claude-squad/kernel"
	"claude-squad/log"
	"claude-squad/program"
	"claude-squad/protected"
	"fmt"
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
func RunDaemon(cfg *config.Config) error {
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
	// and serves the control socket so `cs2 ctl` (and future LLM tools) can
	// drive the fleet. The auto-yes loop below runs alongside.
	//
	// Protected branches (spec decision 7): the daemon has no cwd and no repo
	// of its own (it is a service, C2.2), so it cannot derive "the branch the
	// user is standing on." Instead, protected branches are declared
	// explicitly per repo in the protected store (~/.cs2/protected.json) and
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

	// C4.4: after loading the fleet, demote any instance whose tmux session
	// is gone (e.g. a daemon restart following a tmux crash) from Running to
	// Dead, so the user sees a dead instance instead of a ghost "running".
	// Only a definitive "session absent" from tmux demotes — never a timeout.
	k.ReconcileLiveness()

	// NOTE: the daemon no longer auto-spawns a global orchestrator. The old
	// always-on "instance 0" bootstrap (orchestrator.Ensure at startup + a
	// periodic EnsureLive probe) never worked reliably and is gone. An
	// orchestrator is now spawned explicitly by the user from the TUI via the
	// O key (app.spawnOrchestrator) — same as any other instance. The daemon
	// still owns the kernel/control socket so `cs2 ctl` works; it just no
	// longer has any orchestrator-specific policy.

	socketPath, err := kernel.SocketPath()
	if err != nil {
		return fmt.Errorf("failed to resolve kernel socket path: %w", err)
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	stopCh := make(chan struct{})
	go func() {
		defer wg.Done()
		if err := kernel.Serve(k, socketPath); err != nil {
			log.ErrorLog.Printf("kernel serve failed: %v", err)
		}
	}()

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
					if _, status := instance.HasUpdated(); status == program.StatusReady || status == program.StatusPermission {
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
	for {
		sig := <-sigChan
		if sig == syscall.SIGHUP {
			reloadProtected(protectedStore, k)
			continue
		}
		log.InfoLog.Printf("received signal %s", sig.String())
		break
	}

	// Stop the goroutine so we don't race.
	close(stopCh)
	wg.Wait()

	// Note: the daemon no longer saves instances on shutdown. The kernel is
	// the single writer and persists every mutation as it happens (autosave).
	// A shutdown save here would race the kernel and risk clobbering state it
	// does not know about (the original double-writer bug that orphaned the
	// orchestrator on every restart).
	return nil
}

// LaunchDaemon launches the daemon process. It is concurrency-safe: if a
// daemon is already running OR another launcher is in the middle of starting
// one, LaunchDaemon returns nil without launching a second process. This
// fixes the auto-launch race found in dogfooding: a storm of concurrent
// 'cs2 ctl' calls (with the daemon down) each saw the socket missing and
// each launched its own daemon — up to 5+ processes.
//
// The guard is a lock file (~/.cs2/daemon.lock) created with O_EXCL (atomic
// across processes). The lock carries the launcher's PID so a stale lock
// (from a crashed launcher) is reclaimed. The daemon itself writes the real
// PID to daemon.pid on startup (StopDaemon consumes that).
func LaunchDaemon() error {
	pidDir, err := config.GetConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get config directory: %w", err)
	}

	lockPath := filepath.Join(pidDir, "daemon.lock")
	acquired, err := acquireLaunchLock(lockPath)
	if err != nil {
		return fmt.Errorf("acquire launch lock: %w", err)
	}
	if !acquired {
		// Another launcher is starting (or has started) a daemon. Let it.
		log.InfoLog.Printf("daemon launch already in progress; not launching a second")
		return nil
	}

	// Find the claude squad binary.
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// `cs2 daemon run` is the canonical daemon entrypoint (decision D2): the
	// OS service (Phase 2) and the auto-start both invoke it. The hidden
	// `--daemon` flag remains as a back-compat alias for external scripts.
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

	// Save PID to a file for later management
	pidFile := filepath.Join(pidDir, "daemon.pid")
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0644); err != nil {
		return fmt.Errorf("failed to write PID file: %w", err)
	}

	// Don't wait for the child to exit, it's detached
	return nil
}

// StopDaemon attempts to stop a running daemon process if it exists. Returns no error if the daemon is not found
// (assumes the daemon does not exist).
func StopDaemon() error {
	pidDir, err := config.GetConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get config directory: %w", err)
	}

	pidFile := filepath.Join(pidDir, "daemon.pid")
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

	if err := proc.Kill(); err != nil {
		return fmt.Errorf("failed to stop daemon process: %w", err)
	}

	// Clean up PID file and the launch lock (the lock is the "a daemon exists"
	// sentinel; removing it lets a future LaunchDaemon proceed).
	if err := os.Remove(pidFile); err != nil {
		return fmt.Errorf("failed to remove PID file: %w", err)
	}
	_ = os.Remove(filepath.Join(filepath.Dir(pidFile), "daemon.lock"))

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
