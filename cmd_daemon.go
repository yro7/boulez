package main

import (
	"bufio"
	"claude-squad/config"
	"claude-squad/daemon"
	"claude-squad/kernel"
	"claude-squad/log"
	"claude-squad/protected"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newDaemonCmd builds the `cs2 daemon` subcommand: the first-class lifecycle
// interface to the daemon (the kernel / control authority). Phase 1 of the
// hierarchy inversion: the daemon's lifecycle is detached from the TUI at the
// command level, so it can be installed as an OS service (Phase 2) and so the
// TUI can become a pure client (Phase 3).
//
// `cs2 daemon run` is the canonical foreground entrypoint — the service unit
// (Phase 2) and the TUI/ctl auto-start both invoke this under the hood. The
// other subcommands manage a background daemon.
func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the cs2 daemon (the kernel / control authority)",
		Long: `cs2 daemon manages the daemon process that owns the kernel and serves
the control socket. The daemon is the single writer over fleet state; the
TUI and cs2 ctl are its clients.

Subcommands:
  run     Run the daemon in the foreground (dev / debug). This is the
          canonical entrypoint that the OS service (Phase 2) and auto-start
          invoke.
  start   Start the daemon detached in the background.
  stop    Stop a running daemon.
  status  Report whether the daemon is running (socket + PID).
  log     Print the tail of the daemon log.

  protect <repo> <branch>      Declare a branch protected for a repo (a
                               merge into it is refused kernel-wide).
  unprotect <repo> <branch>   Remove a protected-branch declaration.
  list-protected               List declared protected branches per repo.

The daemon is normally started automatically by the TUI (cs2 / cs2 tui) and
by cs2 ctl when the socket is absent. These subcommands exist for explicit
control and for service installation.`,
	}
	cmd.AddCommand(newDaemonRunCmd())
	cmd.AddCommand(newDaemonStartCmd())
	cmd.AddCommand(newDaemonStopCmd())
	cmd.AddCommand(newDaemonStatusCmd())
	cmd.AddCommand(newDaemonLogCmd())
	cmd.AddCommand(newDaemonProtectCmd())
	cmd.AddCommand(newDaemonUnprotectCmd())
	cmd.AddCommand(newDaemonListProtectedCmd())
	return cmd
}

// newDaemonRunCmd runs the daemon in the foreground. Mirrors what the hidden
// --daemon flag does (kept for back-compat for one release). This is the
// canonical entrypoint the service unit and auto-start invoke.
func newDaemonRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the daemon in the foreground (dev / debug)",
		Long: `Runs the daemon in the foreground. The daemon owns the kernel and serves
the control socket. This is the canonical entrypoint: the OS service
(Phase 2) and the TUI/ctl auto-start both invoke this command.

Use this when debugging a daemon that won't stay up: its stderr/log output
is visible directly.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDaemon()
		},
	}
}

// runDaemon is the single foreground-daemon entrypoint shared by `cs2 daemon
// run` and the hidden `--daemon` back-compat flag. It owns logger setup so a
// foreground daemon logs to the claudesquad log with the [DAEMON] prefix.
func runDaemon() error {
	log.Initialize(true)
	log.SetPrintPathOnClose(false) // daemon: silent, machine-facing
	defer log.Close()

	cfg := config.LoadConfig()
	err := daemon.RunDaemon(cfg)
	if err != nil {
		log.ErrorLog.Printf("daemon exited: %v", err)
	}
	return err
}

// ensureDaemonRunning is the TUI's boot contract (decision D2): the TUI is
// a viewer of the kernel, and there is no degraded mode over a broken daemon.
// If the daemon is already serving it is a no-op; otherwise it auto-starts the
// daemon detached and waits (5s budget) for the socket to come up.
func ensureDaemonRunning() error {
	return daemon.EnsureRunning(5 * time.Second)
}

// printDaemonFailureHint is the TUI boot failure surface (decision D2): when
// the daemon cannot come up, the TUI does not start. This writes the tail of
// the daemon log and the path to `cs2 daemon log` to stderr so the user can
// see why without grepping tmp dirs. The caller returns a non-nil error so
// cobra exits non-zero.
func printDaemonFailureHint() {
	fmt.Fprintln(os.Stderr, "cs2: the daemon could not come up; the TUI will not start.")
	fmt.Fprintln(os.Stderr, "Tail of the daemon log:")
	if tail, err := readTail(log.LogFilePath(), 30); err == nil {
		if tail == "" {
			fmt.Fprintln(os.Stderr, "(log is empty)")
		} else {
			fmt.Fprintln(os.Stderr, tail)
		}
	} else {
		fmt.Fprintf(os.Stderr, "(could not read log: %v)\n", err)
	}
	fmt.Fprintf(os.Stderr, "Full log: %s  (cs2 daemon log)\n", log.LogFilePath())
}

// newDaemonStartCmd launches the daemon detached in the background. Reuses
// LaunchDaemon (O_EXCL launch lock makes a duplicate launch a no-op) and
// waits briefly for the socket to confirm the daemon actually came up.
func newDaemonStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the daemon detached in the background",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			log.SetPrintPathOnClose(false)
			defer log.Close()

			// Already running? Don't launch a second one. The launch lock
			// would make the duplicate a no-op anyway, but reporting "already
			// running" is friendlier and avoids touching the lock file.
			if err := daemon.ProbeSocket(); err == nil {
				pid, _, _ := daemon.ReadPID()
				fmt.Print("daemon already running")
				if pid != 0 {
					fmt.Printf(" (PID %d)", pid)
				}
				fmt.Println()
				return nil
			}

			if err := daemon.LaunchDaemon(); err != nil {
				return fmt.Errorf("failed to start daemon: %w", err)
			}
			socketPath, _ := kernel.SocketPath()
			if err := daemon.WaitForSocket(socketPath, 5*time.Second); err != nil {
				return fmt.Errorf("daemon did not come up (see `cs2 daemon log`): %w", err)
			}
			pid, _, _ := daemon.ReadPID()
			fmt.Print("daemon started")
			if pid != 0 {
				fmt.Printf(" (PID %d)", pid)
			}
			fmt.Println()
			return nil
		},
	}
}

// newDaemonStopCmd stops a running daemon. Reuses StopDaemon, which is a
// no-op if the daemon is not running (missing PID file is not an error).
func newDaemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop a running daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			log.SetPrintPathOnClose(false)
			defer log.Close()

			if err := daemon.StopDaemon(); err != nil {
				return err
			}
			fmt.Println("daemon stopped")
			return nil
		},
	}
}

// newDaemonStatusCmd reports whether the daemon is running by probing the
// socket (a real dial, not just a file check) and reading the PID file. A
// stale PID file with no serving socket is reported as "not running".
func newDaemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Report whether the daemon is running",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			log.SetPrintPathOnClose(false)
			defer log.Close()

			socketPath, _ := kernel.SocketPath()
			reachable := daemon.ProbeSocket() == nil
			pid, hasPid, _ := daemon.ReadPID()

			if reachable {
				fmt.Print("daemon: running")
				if pid != 0 {
					fmt.Printf(" (PID %d)", pid)
				}
				fmt.Println()
			} else {
				fmt.Print("daemon: not running")
				if hasPid && pid != 0 {
					fmt.Printf(" (stale PID file: %d)", pid)
				}
				fmt.Println()
			}
			fmt.Printf("socket: %s\n", socketPath)
			fmt.Printf("log:    %s\n", log.LogFilePath())
			return nil
		},
	}
}

// newDaemonLogCmd prints the tail of the daemon (claudesquad) log. The log is
// shared by the daemon and clients; the tail is the fastest way to see why a
// daemon refused to come up.
func newDaemonLogCmd() *cobra.Command {
	var lines int
	cmd := &cobra.Command{
		Use:   "log",
		Short: "Print the tail of the daemon log",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			log.SetPrintPathOnClose(false)
			defer log.Close()

			tail, err := readTail(log.LogFilePath(), lines)
			if err != nil {
				return fmt.Errorf("read log: %w", err)
			}
			if tail == "" {
				fmt.Printf("(log %s is empty)\n", log.LogFilePath())
				return nil
			}
			fmt.Print(tail)
			if !strings.HasSuffix(tail, "\n") {
				fmt.Println()
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "number of trailing lines to print")
	return cmd
}

// readTail returns the last n lines of the file at path. Used by `cs2 daemon
// log` and by printDaemonFailureHint (TUI boot failure, C1.3). Reads the
// whole file and slices; the claudesquad log is small and this is a
// human-facing path, not a hot one.
func readTail(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

// newDaemonProtectCmd declares a branch protected for a repo (C2.1). The
// declaration is per-repo on disk; the daemon picks it up at boot and on
// SIGHUP. Protected branches are enforced kernel-wide (a branch protected
// for any repo is refused for all repos) — see the protected package docs.
func newDaemonProtectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "protect <repo> <branch>",
		Short: "Declare a branch protected for a repo",
		Long: `Declares a branch protected for the given repo. The daemon refuses any
merge (or land) into a protected branch, kernel-wide, on top of the
Merger's conventional main/master guard.

The declaration is per-repo on disk. The daemon reads the protected store
at boot and on SIGHUP, so a protect/unprotect takes effect after a SIGHUP
(or a daemon restart).

The repo path may be relative; it is resolved to absolute.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			log.SetPrintPathOnClose(false)
			defer log.Close()

			store, err := protected.New()
			if err != nil {
				return fmt.Errorf("open protected store: %w", err)
			}
			if err := store.Add(args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("protected %q for %q (reload daemon: kill -HUP <daemon-pid>)\n", args[1], args[0])
			return nil
		},
	}
}

// newDaemonUnprotectCmd removes a protected-branch declaration (C2.1).
func newDaemonUnprotectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unprotect <repo> <branch>",
		Short: "Remove a protected-branch declaration",
		Long: `Removes a protected-branch declaration for the given repo. Removing a
branch that was not protected is a no-op.

The change takes effect after a SIGHUP (or a daemon restart).`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			log.SetPrintPathOnClose(false)
			defer log.Close()

			store, err := protected.New()
			if err != nil {
				return fmt.Errorf("open protected store: %w", err)
			}
			if err := store.Remove(args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("unprotected %q for %q (reload daemon: kill -HUP <daemon-pid>)\n", args[1], args[0])
			return nil
		},
	}
}

// newDaemonListProtectedCmd lists declared protected branches per repo (C2.1).
func newDaemonListProtectedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list-protected",
		Short: "List declared protected branches per repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			log.SetPrintPathOnClose(false)
			defer log.Close()

			store, err := protected.New()
			if err != nil {
				return fmt.Errorf("open protected store: %w", err)
			}
			all, err := store.Load()
			if err != nil {
				return err
			}
			repos := make([]string, 0, len(all))
			for r := range all {
				repos = append(repos, r)
			}
			sort.Strings(repos)
			if len(repos) == 0 {
				fmt.Println("(no protected branches declared)")
				fmt.Printf("store: %s\n", store.Path())
				return nil
			}
			for _, r := range repos {
				branches := append([]string(nil), all[r]...)
				sort.Strings(branches)
				fmt.Printf("%s:\n", r)
				for _, b := range branches {
					fmt.Printf("  - %s\n", b)
				}
			}
			fmt.Printf("store: %s\n", store.Path())
			return nil
		},
	}
}
