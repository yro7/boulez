package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"claude-squad/daemon"
	"claude-squad/kernel"
	"claude-squad/log"
	"github.com/spf13/cobra"
)

// newCtlCmd builds the `cs2 ctl` subcommand: a thin client that sends one
// syscall to the kernel over the control socket and prints the JSON response.
// It is the human/programmatic face of the control API. The LLM's tools
// (Shape B) will wrap these same syscalls.
//
// If the daemon is not running, ctl auto-launches it (the daemon is the
// canonical "always up during cs2 use" process) then retries.
func newCtlCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ctl <method> [--param value ...]",
		Short: "Send a control syscall to the running kernel (programmatic fleet control)",
		Long: `cs2 ctl sends a single JSON-RPC syscall to the kernel's control socket
and prints the JSON response. This is the low-level control API: spawn
workers, send prompts, merge branches, list instances, etc.

The daemon (kernel) must be running. If it isn't, ctl auto-launches it.

Examples:
  cs2 ctl list_instances
  cs2 ctl spawn_worker --repo /path/to/repo --prompt "fix the bug" --program bash
  cs2 ctl get_instance --id <uuid>
  cs2 ctl merge --target-repo /path --target-branch integration --source feat-a,feat-b
`,
	}
	cmd.AddCommand(newCtlListCmd())
	cmd.AddCommand(newCtlSpawnCmd())
	cmd.AddCommand(newCtlGetInstanceCmd())
	cmd.AddCommand(newCtlSendPromptCmd())
	cmd.AddCommand(newCtlPauseCmd())
	cmd.AddCommand(newCtlResumeCmd())
	cmd.AddCommand(newCtlKillCmd())
	cmd.AddCommand(newCtlMergeCmd())
	return cmd
}

// rawCtl sends a Request and prints the Response. Shared by all subcommands.
// asJSON controls whether a success result is pretty-printed as JSON (true)
// or rendered as a compact one-liner (false). Errors are always JSON.
func rawCtl(req kernel.Request) error {
	// The ctl path doesn't go through the root command's log.Initialize, so
	// ensure the logger is up before LaunchDaemon uses it.
	log.Initialize(false)
	defer log.Close()

	socketPath, err := kernel.SocketPath()
	if err != nil {
		return fmt.Errorf("resolve socket path: %w", err)
	}

	resp, err := kernel.Call(socketPath, req)
	if err != nil {
		// Daemon down? Auto-launch and retry once.
		if launchErr := daemon.LaunchDaemon(); launchErr != nil {
			return fmt.Errorf("kernel unreachable and auto-launch failed: %w (launch: %v)", err, launchErr)
		}
		// Wait for the daemon to bind the socket (rather than a blind sleep).
		// Concurrent ctl callers that lost the launch lock will also wait here.
		if waitErr := daemon.WaitForSocket(socketPath, 3*time.Second); waitErr != nil {
			return fmt.Errorf("kernel unreachable after auto-launch: %w", waitErr)
		}
		resp, err = kernel.Call(socketPath, req)
		if err != nil {
			return fmt.Errorf("kernel call after auto-launch: %w", err)
		}
	}

	if resp.Error != nil {
		// Print structured error to stderr; non-zero exit.
		b, _ := json.MarshalIndent(resp.Error, "", "  ")
		fmt.Fprintln(os.Stderr, string(b))
		os.Exit(1)
	}

	// Success: pretty-print the result.
	var pretty interface{}
	if err := json.Unmarshal(resp.Result, &pretty); err == nil {
		b, _ := json.MarshalIndent(pretty, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Println(string(resp.Result))
	}
	return nil
}

// --- subcommands ---

func newCtlListCmd() *cobra.Command {
	var kind, status, repo string
	cmd := &cobra.Command{
		Use:   "list_instances",
		Short: "List instances in the fleet",
		RunE: func(cmd *cobra.Command, args []string) error {
			params := map[string]interface{}{}
			if kind != "" {
				params["kind"] = kindInt(kind)
			}
			if status != "" {
				params["status"] = statusInt(status)
			}
			if repo != "" {
				params["repo"] = repo
			}
			return rawCtl(kernel.Request{Method: "list_instances", Params: mustJSON(params)})
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "filter by kind (worker|orchestrator)")
	cmd.Flags().StringVar(&status, "status", "", "filter by status (running|ready|loading|paused)")
	cmd.Flags().StringVar(&repo, "repo", "", "filter by repo name")
	return cmd
}

func newCtlSpawnCmd() *cobra.Command {
	var repo, branch, prompt, program, title, kind string
	cmd := &cobra.Command{
		Use:   "spawn_worker",
		Short: "Spawn a new worker instance",
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo == "" {
				return fmt.Errorf("--repo is required")
			}
			params := map[string]interface{}{
				"repo": repo,
			}
			if branch != "" {
				params["branch"] = branch
			}
			if prompt != "" {
				params["prompt"] = prompt
			}
			if program != "" {
				params["program"] = program
			}
			if title != "" {
				params["title"] = title
			}
			if kind != "" {
				params["kind"] = kindInt(kind)
			}
			return rawCtl(kernel.Request{Method: "spawn_worker", Params: mustJSON(params)})
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "repository path (required)")
	cmd.Flags().StringVar(&branch, "branch", "", "existing branch to start on (default: new branch from HEAD)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "initial prompt to send after start")
	cmd.Flags().StringVar(&program, "program", "", "agent command (default: claude)")
	cmd.Flags().StringVar(&title, "title", "", "instance title (default: auto-derived)")
	cmd.Flags().StringVar(&kind, "kind", "worker", "instance kind (worker|orchestrator)")
	return cmd
}

func newCtlGetInstanceCmd() *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "get_instance",
		Short: "Get details of an instance by ID",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			return rawCtl(kernel.Request{Method: "get_instance", Params: mustJSON(map[string]string{"id": id})})
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "instance ID (required)")
	return cmd
}

func newCtlSendPromptCmd() *cobra.Command {
	var id, prompt string
	cmd := &cobra.Command{
		Use:   "send_prompt",
		Short: "Send a prompt to an instance by ID",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" || prompt == "" {
				return fmt.Errorf("--id and --prompt are required")
			}
			return rawCtl(kernel.Request{Method: "send_prompt", Params: mustJSON(map[string]string{"id": id, "prompt": prompt})})
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "instance ID (required)")
	cmd.Flags().StringVar(&prompt, "prompt", "", "prompt text (required)")
	return cmd
}

func newCtlPauseCmd() *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "pause",
		Short: "Pause an instance by ID",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			return rawCtl(kernel.Request{Method: "pause", Params: mustJSON(map[string]string{"id": id})})
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "instance ID (required)")
	return cmd
}

func newCtlResumeCmd() *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "resume",
		Short: "Resume a paused instance by ID",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			return rawCtl(kernel.Request{Method: "resume", Params: mustJSON(map[string]string{"id": id})})
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "instance ID (required)")
	return cmd
}

func newCtlKillCmd() *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "kill",
		Short: "Kill an instance by ID",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			return rawCtl(kernel.Request{Method: "kill", Params: mustJSON(map[string]string{"id": id})})
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "instance ID (required)")
	return cmd
}

func newCtlMergeCmd() *cobra.Command {
	var targetRepo, targetBranch, sources string
	cmd := &cobra.Command{
		Use:   "merge",
		Short: "Merge source branches into a target branch",
		RunE: func(cmd *cobra.Command, args []string) error {
			if targetRepo == "" || targetBranch == "" || sources == "" {
				return fmt.Errorf("--target-repo, --target-branch and --source are required")
			}
			params := map[string]interface{}{
				"target_repo":     targetRepo,
				"target_branch":   targetBranch,
				"source_branches": strings.Split(sources, ","),
			}
			return rawCtl(kernel.Request{Method: "merge", Params: mustJSON(params)})
		},
	}
	cmd.Flags().StringVar(&targetRepo, "target-repo", "", "repository path (required)")
	cmd.Flags().StringVar(&targetBranch, "target-branch", "", "branch to merge INTO (required)")
	cmd.Flags().StringVar(&sources, "source", "", "comma-separated source branches (required)")
	return cmd
}

// --- helpers ---

// mustJSON marshals v or panics. Used by ctl subcommands which build params
// from flags; a marshal failure is a programming error, not a runtime one.
func mustJSON(v interface{}) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("ctl: marshal params: %v", err))
	}
	return b
}

// kindInt maps a string to the wire int for session.Kind. The wire carries
// ints (the iota values); the kernel decodes them. Strings are friendlier
// for humans at the CLI.
func kindInt(s string) int {
	switch strings.ToLower(s) {
	case "orchestrator", "orch":
		return 1 // session.KindOrchestrator
	default:
		return 0 // session.KindWorker
	}
}

// statusInt maps a string to the wire int for session.Status.
func statusInt(s string) int {
	switch strings.ToLower(s) {
	case "ready":
		return 1
	case "loading":
		return 2
	case "paused":
		return 3
	default:
		return 0 // Running
	}
}
