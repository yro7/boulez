package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/yro7/boulez/app"
	"github.com/yro7/boulez/cli"
	cmd2 "github.com/yro7/boulez/cmd"
	"github.com/yro7/boulez/config"
	"github.com/yro7/boulez/daemon"
	"github.com/yro7/boulez/log"
	"github.com/yro7/boulez/session/git"
	"github.com/yro7/boulez/session/tmux"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	version     = "1.0.19"
	programFlag string
	autoYesFlag bool
	binName     string
	rootCmd     = &cobra.Command{
		Use:   "boulez",
		Short: "Boulez - Orchestrate multiple AI coding agents (Claude Code, Codex, Pi, Aider, …) in isolated git worktrees.",
		RunE:  runTUI,
	}

	tuiCmd = &cobra.Command{
		Use:   "tui",
		Short: "Launch the Boulez TUI (default)",
		RunE:  runTUI,
	}

	resetCmd = &cobra.Command{
		Use:   "reset",
		Short: "Reset all stored instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			log.SetPrintPathOnClose(true) // human-facing: surface log path on exit
			defer log.Close()

			state := config.LoadState()
			if err := state.DeleteAllInstances(); err != nil {
				return fmt.Errorf("failed to reset storage: %w", err)
			}
			fmt.Println("Storage has been reset successfully")

			if err := tmux.CleanupSessions(cmd2.MakeExecutor()); err != nil {
				return fmt.Errorf("failed to cleanup tmux sessions: %w", err)
			}
			fmt.Println("Tmux sessions have been cleaned up")

			if err := git.CleanupWorktrees(); err != nil {
				return fmt.Errorf("failed to cleanup worktrees: %w", err)
			}
			fmt.Println("Worktrees have been cleaned up")

			// Kill any daemon that's running.
			if err := daemon.StopDaemon(); err != nil {
				return err
			}
			fmt.Println("daemon has been stopped")

			return nil
		},
	}

	debugCmd = &cobra.Command{
		Use:   "debug",
		Short: "Print debug information like config paths",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			log.SetPrintPathOnClose(true) // human-facing: surface log path on exit
			defer log.Close()

			cfg := config.LoadConfig()

			configDir, err := config.GetConfigDir()
			if err != nil {
				return fmt.Errorf("failed to get config directory: %w", err)
			}
			configJson, _ := json.MarshalIndent(cfg, "", "  ")

			fmt.Printf("Config: %s\n%s\n", filepath.Join(configDir, config.ConfigFileName), configJson)

			return nil
		},
	}

	versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("%s version %s\n", binName, version)
			fmt.Printf("https://github.com/yro7/boulez/releases/tag/v%s\n", version)
		},
	}
)

func init() {
	rootCmd.Flags().StringVarP(&programFlag, "program", "p", "",
		"Program to run in new instances (e.g. 'aider --model ollama_chat/gemma3:1b')")
	rootCmd.Flags().BoolVarP(&autoYesFlag, "autoyes", "y", false,
		"[experimental] If enabled, all instances will automatically accept prompts")

	rootCmd.AddCommand(tuiCmd)
	rootCmd.AddCommand(debugCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(resetCmd)
	rootCmd.AddCommand(cli.NewCtlCmd())
	rootCmd.AddCommand(cli.NewDaemonCmd())
	rootCmd.AddCommand(cli.NewRepoImportCmd())
}

// runTUI is the entrypoint for both the bare `boulez` invocation and the
// explicit `boulez tui` subcommand. It ensures the daemon (the kernel / control
// authority) is reachable, then starts the TUI as a viewer of it.
func runTUI(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	log.Initialize(false)
	log.SetPrintPathOnClose(true) // interactive: surface log path on exit
	defer log.Close()

	cfg := config.LoadConfig()

	// Program flag overrides config
	program := cfg.GetProgram()
	if programFlag != "" {
		program = programFlag
	}
	// AutoYes flag overrides config
	autoYes := cfg.AutoYes
	if autoYesFlag {
		autoYes = true
	}
	// Ensure the daemon (the kernel / control authority) is reachable.
	// The TUI is a viewer of the kernel: no kernel, no viewer (decision
	// D2). If the socket is absent we auto-start the daemon detached;
	// if it does not come up within the timeout we fail loud — print the
	// daemon log tail and the path to `boulez daemon log` and exit
	// non-zero. There is no degraded TUI mode over a broken daemon.
	//
	// The daemon's parent during the transition is this Setsid-detached
	// child; after Phase 2 it is launchd/systemd. The TUI's job is to
	// ensure the daemon is reachable, not to be its parent (C1.3/C1.4).
	if err := cli.EnsureDaemonRunning(); err != nil {
		cli.PrintDaemonFailureHint()
		return fmt.Errorf("daemon not reachable: %w", err)
	}

	return app.Run(ctx, program, autoYes)
}

func main() {
	// Extract the binary name from how this was invoked
	binName = filepath.Base(os.Args[0])
	rootCmd.Use = binName

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
	}
}
