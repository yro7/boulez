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
	cmd.AddCommand(newCtlAsCmd())
	return cmd
}

// newCtlAsCmd builds `cs2 ctl as <instance-id> <syscall> [...]`: it
// authenticates the connection as the given instance, then issues the
// syscall on the SAME connection so the caller identity is bound. This is
// how a plan is recorded for an orchestrator via the CLI (finding #4): the
// orchestrator's `spawn_worker` calls are attributed to it, so its plan.json
// is written. Without `as`, every `cs2 ctl` call is top-level (no plan).
//
// Only syscalls whose effect depends on the caller identity need `as`:
// spawn_worker (records the worker in the caller's plan) and merge (records
// the merge target). The other syscalls are caller-agnostic.
func newCtlAsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "as <instance-id> <syscall> [--param value ...]",
		Short: "Issue a syscall authenticated as an instance (records the caller's plan)",
		Long: `cs2 ctl as authenticates the connection as the given instance, then
issues the named syscall on the same connection. This binds the caller
identity so the recursion guards apply and the orchestrator's plan is
recorded (resumability substrate).

Only spawn_worker and merge are caller-aware; others ignore the binding.

Example:
  cs2 ctl as <orch-id> spawn_worker --repo /r --program bash --prompt "task"
  cs2 ctl as <orch-id> merge --target-repo /r --target-branch integ --source feat-a`,
		Args: cobra.MinimumNArgs(2),
		// DisableFlagParsing: the 'as' command must NOT parse flags itself —
		// they belong to the wrapped syscall (e.g. --repo). Without this,
		// cobra eats --repo on the 'as' command and the subcommand never sees it.
		DisableFlagParsing: true,
		RunE: runCtlAs,
	}
	return cmd
}

// runCtlAs dispatches to the named subcommand after authenticating. It builds
// the syscall's params by re-using the subcommand's flag set, then sends
// `authenticate` + the syscall on one connection via rawCtlSession.
func runCtlAs(cmd *cobra.Command, args []string) error {
	instanceID := args[0]
	syscallName := args[1]
	syscallArgs := args[2:]

	// Build the matching subcommand and parse the syscall's flags.
	sub := buildCtlSub(syscallName)
	if sub == nil {
		return fmt.Errorf("unknown or unsupported syscall for 'as': %s (only spawn_worker and merge are caller-aware)", syscallName)
	}
	sub.SetArgs(syscallArgs)
	if err := sub.ParseFlags(syscallArgs); err != nil {
		return fmt.Errorf("parse flags for %s: %w", syscallName, err)
	}

	// Build the syscall Request via the subcommand's captured params.
	req, err := buildCtlRequest(sub, syscallName)
	if err != nil {
		return err
	}

	// Authenticate + syscall on one connection (same session).
	authParams := mustJSON(map[string]string{"instance_id": instanceID})
	return rawCtlSession([]kernel.Request{
		{Method: "authenticate", Params: authParams},
		req,
	})
}

// rawCtl sends a Request and prints the Response. Shared by all subcommands.
// asJSON controls whether a success result is pretty-printed as JSON (true)
// or rendered as a compact one-liner (false). Errors are always JSON.
func rawCtl(req kernel.Request) error {
	return rawCtlSession([]kernel.Request{req})
}

// rawCtlSession sends a sequence of requests on a SINGLE connection (so they
// share a session — e.g. authenticate + spawn_worker) and prints the LAST
// response. The earlier responses are expected to be {ok:true}; only the
// final syscall's result is shown to the user. Used by `cs2 ctl as`.
func rawCtlSession(reqs []kernel.Request) error {
	// Capture hook: `cs2 ctl as` uses this to grab the request without sending
	// it, so it can prepend an `authenticate` on the same session.
	if captureHook != nil {
		return captureHook(reqs)
	}

	// The ctl path doesn't go through the root command's log.Initialize, so
	// ensure the logger is up before LaunchDaemon uses it.
	log.Initialize(false)
	defer log.Close()

	socketPath, err := kernel.SocketPath()
	if err != nil {
		return fmt.Errorf("resolve socket path: %w", err)
	}

	resps, err := kernel.CallSession(socketPath, reqs)
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
		resps, err = kernel.CallSession(socketPath, reqs)
		if err != nil {
			return fmt.Errorf("kernel call after auto-launch: %w", err)
		}
	}

	// The last response is the syscall's result; earlier ones (e.g.
	// authenticate) are {ok:true} and not shown.
	resp := resps[len(resps)-1]
	if resp.Error != nil {
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
				params["kind"] = kindWire(kind)
			}
			if status != "" {
				params["status"] = statusWire(status)
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
	var branchExisting bool
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
			if branchExisting {
				params["branch_must_exist"] = true
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
				params["kind"] = kindWire(kind)
			}
			return rawCtl(kernel.Request{Method: "spawn_worker", Params: mustJSON(params)})
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "repository path (required)")
	cmd.Flags().StringVar(&branch, "branch", "", "branch to start on; created from HEAD if absent (default: new branch from HEAD when omitted)")
	cmd.Flags().BoolVar(&branchExisting, "branch-existing", false, "require --branch to pre-exist (resume an existing branch); errors BRANCH_NOT_FOUND if absent")
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

// kindWire maps a CLI string to the wire value for session.Kind. The wire
// accepts both strings ("worker"/"orchestrator") and ints; we pass the string
// so the request is self-documenting. The kernel's UnmarshalJSON handles it.
func kindWire(s string) string {
	switch strings.ToLower(s) {
	case "orchestrator", "orch":
		return "orchestrator"
	default:
		return "worker"
	}
}

// statusWire maps a CLI string to the wire value for session.Status. Same
// rationale as kindWire: pass the string, let the kernel parse.
func statusWire(s string) string {
	switch strings.ToLower(s) {
	case "ready":
		return "ready"
	case "loading":
		return "loading"
	case "paused":
		return "paused"
	default:
		return "running"
	}
}

// buildCtlSub returns a cobra subcommand for the named syscall, configured
// with its flags but NOT yet executed. Used by `cs2 ctl as` to parse a
// syscall's flags and build its request without sending it (the send happens
// via rawCtlSession after authenticate). Returns nil for unsupported
// syscalls (only the caller-aware ones make sense under `as`).
func buildCtlSub(name string) *cobra.Command {
	switch name {
	case "spawn_worker":
		return newCtlSpawnCmd()
	case "merge":
		return newCtlMergeCmd()
	default:
		return nil
	}
}

// buildCtlRequest extracts the syscall Request a subcommand WOULD send, by
// running its RunE logic against a capture buffer instead of the socket. We
// achieve the capture by temporarily swapping rawCtlSession for a recorder
// via a package-level hook. This avoids duplicating each subcommand's param
// construction.
var captureHook func([]kernel.Request) error

func buildCtlRequest(sub *cobra.Command, name string) (kernel.Request, error) {
	var captured []kernel.Request
	prev := captureHook
	captureHook = func(reqs []kernel.Request) error {
		captured = reqs
		return nil
	}
	defer func() { captureHook = prev }()

	// Execute the subcommand's RunE: it calls rawCtlSession (which checks
	// captureHook first) and we grab the request without sending it.
	if err := sub.RunE(sub, []string{}); err != nil {
		// An arg-validation error after capture is fine: the subcommand built
		// and captured the request before erroring (e.g. a missing optional
		// flag it checked post-capture). If nothing was captured, surface the err.
		if len(captured) == 0 {
			return kernel.Request{}, err
		}
	}
	if len(captured) == 0 {
		return kernel.Request{}, fmt.Errorf("could not capture %s request", name)
	}
	return captured[0], nil
}
