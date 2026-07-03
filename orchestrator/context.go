// Package orchestrator is the "instance 0" layer: the always-on global
// orchestrator that consumes cs2's control API (the kernel syscalls) to
// supervise the fleet.
//
// cs2 is agent-agnostic: the orchestrator is an ordinary Instance of
// KindOrchestrator running some agent program (Pi by user choice, but any
// terminal agent works). This package does NOT know which agent is running —
// it only (a) writes a context file (ORCHESTRATOR.md) into the orchestrator's
// control dir documenting the `cs2 ctl` tool surface + the orchestrator's
// own ID, and (b) injects a one-time fleet snapshot into the agent's pane via
// SendPrompt. After that the agent is supervised: it calls `cs2 ctl` shell
// tools to drive the fleet at its own pace.
//
// The package is deliberately decoupled from the kernel: it defines a minimal
// API interface (ListInstances / SpawnOrchestrator / SendPrompt) so the Ensure
// logic is unit-testable without tmux, without a real LLM, without even the
// kernel — exactly the testability property the kernel package established.
package orchestrator

import (
	"claude-squad/config"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ContextFileName is the name of the context file written into the
// orchestrator's control dir. The agent reads it from its cwd (the control
// dir is the headless worktree's working directory).
const ContextFileName = "ORCHESTRATOR.md"

// ControlDir returns the orchestrator's control directory
// (~/.cs2/orchestrators/<id>/). It is the cwd of the orchestrator's tmux
// session and where ORCHESTRATOR.md + plan.json live.
func ControlDir(id string) (string, error) {
	orchDir, err := config.OrchestratorsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(orchDir, id), nil
}

// WriteContextFile writes ORCHESTRATOR.md into the orchestrator's control dir.
// Safe to call repeatedly (on every cs2 restart): it overwrites with the
// current documentation, so the agent always sees the up-to-date tool surface
// even across cs2 version upgrades. The control dir is created by the
// headless worktree at instance start; this function ensures it exists too
// (defensive).
func WriteContextFile(id string) error {
	dir, err := ControlDir(id)
	if err != nil {
		return fmt.Errorf("resolve control dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create control dir: %w", err)
	}
	path := filepath.Join(dir, ContextFileName)
	if err := os.WriteFile(path, []byte(ContextContent(id)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", ContextFileName, err)
	}
	return nil
}

// ContextContent builds the body of ORCHESTRATOR.md. It documents:
//   - the orchestrator's role and its own instance ID (so it can
//     `cs2 ctl as <self> ...`),
//   - the full `cs2 ctl` tool surface (the syscalls as shell commands),
//   - the structured error codes it should branch on,
//   - a reminder that the fleet snapshot was injected once and that it can
//     re-fetch state with `list_instances`.
//
// This is documentation, kept agent-agnostic. It describes the `cs2 ctl` CLI
// surface (what the agent actually shells out to), not the kernel's JSON-RPC
// surface.
func ContextContent(id string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# cs2 orchestrator\n\n")
	fmt.Fprintf(&b, "You are the **cs2 global orchestrator**. You supervise a fleet of\n")
	fmt.Fprintf(&b, "worker instances (coding agents in isolated git worktrees): you spawn\n")
	fmt.Fprintf(&b, "them, give them tasks, observe their progress, and merge their branches.\n\n")

	fmt.Fprintf(&b, "## Your identity\n\n")
	fmt.Fprintf(&b, "Your instance ID is:\n\n")
	fmt.Fprintf(&b, "```\n%s\n```\n\n", id)
	fmt.Fprintf(&b, "Use it with `cs2 ctl as <id> <syscall>` so your actions are attributed to\n")
	fmt.Fprintf(&b, "your plan (this is how cs2 records what you spawned/merged, for resumability).\n\n")

	fmt.Fprintf(&b, "## Your tools\n\n")
	fmt.Fprintf(&b, "You drive the fleet by shelling out to `cs2 ctl`. Every command prints a\n")
	fmt.Fprintf(&b, "single JSON document on stdout (parseable with `jq` or your language's JSON\n")
	fmt.Fprintf(&b, "parser). Errors are printed to stderr as `{\"code\": \"...\", \"message\": \"...\"}`\n")
	fmt.Fprintf(&b, "and exit non-zero.\n\n")
	fmt.Fprintf(&b, "If the daemon is down, `cs2 ctl` auto-launches it and retries — you do not\n")
	fmt.Fprintf(&b, "need to manage the daemon.\n\n")

	fmt.Fprintf(&b, "### Read (observe the fleet)\n\n")
	fmt.Fprintf(&b, "```\n# list all instances (optional filters)\ncs2 ctl list_instances [--kind worker|orchestrator] [--status running|ready|loading|paused] [--repo <name>]\n\n# full detail of one instance: status + diff + tmux scrollback\ncs2 ctl get_instance --id <instance-id>\n```\n\n")

	fmt.Fprintf(&b, "### Mutate (lifecycle)\n\n")
	fmt.Fprintf(&b, "```\n# spawn a worker. --branch is created from HEAD if absent (deterministic\n# names); --branch-existing requires it to pre-exist.\ncs2 ctl as %s spawn_worker --repo <path> --program <cmd> --prompt \"<task>\" [--branch <name>] [--title <t>]\n\n# send more instructions to a running instance\ncs2 ctl send_prompt --id <instance-id> --prompt \"<text>\"\n\n# pause / resume / kill an instance (by ID)\ncs2 ctl pause --id <id>\ncs2 ctl resume --id <id>\ncs2 ctl kill --id <id>\n```\n\n", id)

	fmt.Fprintf(&b, "### Orchestrate (merge)\n\n")
	fmt.Fprintf(&b, "```\n# merge source branches into a target branch of a repo.\n# --source is comma-separated. The target must NOT be a protected branch\n# (main/master/the host repo's current branch).\ncs2 ctl as %s merge --target-repo <path> --target-branch <branch> --source <b1>,<b2>\n```\n\n", id)

	fmt.Fprintf(&b, "### Error codes\n\n")
	fmt.Fprintf(&b, "Branch on `code` rather than parsing messages:\n\n")
	fmt.Fprintf(&b, "| code | meaning |\n")
	fmt.Fprintf(&b, "|---|---|\n")
	fmt.Fprintf(&b, "| `UNKNOWN_INSTANCE` | the given instance ID does not exist |\n")
	fmt.Fprintf(&b, "| `WORKER_CANNOT_SPAWN` | a worker tried to spawn (topology is two-level) |\n")
	fmt.Fprintf(&b, "| `NESTED_ORCHESTRATOR` | an orchestrator tried to spawn an orchestrator |\n")
	fmt.Fprintf(&b, "| `PROTECTED_BRANCH` | merge target is protected (main/master/host-current) |\n")
	fmt.Fprintf(&b, "| `BRANCH_NOT_FOUND` | `--branch-existing` set but branch absent |\n")
	fmt.Fprintf(&b, "| `INTERNAL` | unexpected server error |\n\n")

	fmt.Fprintf(&b, "## Fleet state\n\n")
	fmt.Fprintf(&b, "A snapshot of the fleet was injected into your conversation **once**, at\n")
	fmt.Fprintf(&b, "startup. It is now stale. To see the current state, call:\n\n")
	fmt.Fprintf(&b, "```\ncs2 ctl list_instances\n```\n\n")
	fmt.Fprintf(&b, "You are supervised, not autonomous. After refreshing the fleet state above,\n")
	fmt.Fprintf(&b, "STOP and wait for an explicit task (a human attaching to your pane, or a\n")
	fmt.Fprintf(&b, "`cs2 ctl send_prompt --id <your-id>`). Do NOT spawn, merge, or send prompts\n")
	fmt.Fprintf(&b, "to other instances on your own initiative. Execute the one task you are\n")
	fmt.Fprintf(&b, "given, then stop and wait again. Do not loop looking for more work to do.\n")

	return b.String()
}
