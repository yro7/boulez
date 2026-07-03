package daemon

import (
	"claude-squad/config"
	"claude-squad/kernel"
	"claude-squad/log"
	"claude-squad/program"
	"claude-squad/session"
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
	storage, err := session.NewStorage(state)
	if err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}

	instances, err := storage.LoadInstances()
	if err != nil {
		return fmt.Errorf("failed to load instacnes: %w", err)
	}
	// AutoYes is per-instance (persisted on InstanceData). The daemon respects
	// the stored value rather than forcing it globally — this lets remote
	// instances stay off by default while local ones follow the user's choice.

	// The kernel is the single-writer control authority. The daemon owns it
	// and serves the control socket so `cs2 ctl` (and future LLM tools) can
	// drive the fleet. The auto-yes loop below runs alongside.
	//
	// Inject the host repo's current branch as a kernel-level protected
	// branch (spec decision 7): an orchestrator may never merge INTO the
	// branch the user is actively standing on — that would clobber their
	// working tree. The Merger cannot see the host repo, so this guard lives
	// in the kernel (non-contournable by the client). Resolved once here, at
	// daemon startup, from the cwd the user launched cs2 from.
	protected := resolveHostProtectedBranches()
	k := kernel.New(storage, kernel.WithSpawner(kernelSpawner{}), kernel.WithMerger(realMerger{}), kernel.WithProtectedBranches(protected))
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
			for _, instance := range instances {
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

	// Notify on SIGINT (Ctrl+C) and SIGTERM. Save instances before
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan
	log.InfoLog.Printf("received signal %s", sig.String())

	// Stop the goroutine so we don't race.
	close(stopCh)
	wg.Wait()

	if err := storage.SaveInstances(instances); err != nil {
		log.ErrorLog.Printf("failed to save instances when terminating daemon: %v", err)
	}
	return nil
}

// LaunchDaemon launches the daemon process.
func LaunchDaemon() error {
	// Find the claude squad binary.
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	cmd := exec.Command(execPath, "--daemon")

	// Detach the process from the parent
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	// Set process group to prevent signals from propagating
	cmd.SysProcAttr = getSysProcAttr()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start child process: %w", err)
	}

	log.InfoLog.Printf("started daemon child process with PID: %d", cmd.Process.Pid)

	// Save PID to a file for later management
	pidDir, err := config.GetConfigDir()
	if err != nil {
		return fmt.Errorf("failed to get config directory: %w", err)
	}

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

	// Clean up PID file
	if err := os.Remove(pidFile); err != nil {
		return fmt.Errorf("failed to remove PID file: %w", err)
	}

	log.InfoLog.Printf("daemon process (PID: %d) stopped successfully", pid)
	return nil
}

// resolveHostProtectedBranches returns the host repo's currently checked-out
// branch, so the kernel can refuse merges into it (spec decision 7). It uses
// the daemon process's cwd — which is the directory the user launched cs2
// from (the daemon inherits the parent's cwd). On any error (not a git repo,
// detached HEAD, git missing) it returns nil: the conventional main/master
// guard still applies via the Merger, so failing open is safe.
func resolveHostProtectedBranches() []string {
	cwd, err := os.Getwd()
	if err != nil {
		log.WarningLog.Printf("could not resolve cwd for host-branch guard: %v", err)
		return nil
	}
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		// Not a git repo, or git unavailable — no host branch to protect.
		return nil
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" || branch == "HEAD" {
		// detached HEAD — no branch name to protect.
		return nil
	}
	log.InfoLog.Printf("host repo %s is on branch %q; protecting it from merges", cwd, branch)
	return []string{branch}
}
