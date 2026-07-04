package main

import (
	"claude-squad/config"
	"claude-squad/daemon"
	"claude-squad/log"
	"fmt"

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
	}
	cmd.AddCommand(newDaemonRunCmd())
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

// ensureDaemonRunning is the TUI's boot contract (decision D2): the TUI is a
// viewer of the kernel, and there is no degraded mode over a broken daemon.
// Implemented in C1.3; stubbed here so the TUI boot compiles during the
// incremental commits.
func ensureDaemonRunning() error {
	return nil
}

// printDaemonFailureHint is the TUI boot failure surface (decision D2). It
// writes the tail of the daemon log + the path to `cs2 daemon log` to stderr
// so the user can see why the daemon refused to come up. Implemented in C1.3.
func printDaemonFailureHint() {
	fmt.Println("cs2: the daemon could not come up; the TUI will not start.")
}
