// Package orchestrator holds the context an orchestrator instance needs at
// spawn time: the ORCHESTRATOR.md role doc, the .mcp.json MCP client config,
// and the one-time fleet snapshot injected into the agent's pane. The
// orchestrator is an ordinary fleet instance (KindOrchestrator, headless
// worktree) that consumes boulez's control API (the kernel syscalls) to
// supervise the fleet. It is spawned on demand by the user from the TUI
// (O key), NOT auto-spawned at startup.
//
// boulez is agent-agnostic: the orchestrator runs some agent program (Pi by
// user choice, but any terminal agent works). This package does NOT know
// which agent is running — it only (a) writes ORCHESTRATOR.md + .mcp.json
// into the orchestrator's control dir documenting the role + MCP tool
// surface, and (b) injects a one-time fleet snapshot into the agent's pane
// via RenderFleet + InjectionPrompt. After that the agent is supervised:
// if it supports MCP (Claude Code, Codex), it discovers the `boulez` MCP
// server via .mcp.json and calls typed tools; otherwise it can still shell
// out to `boulez ctl`.
//
// The package is deliberately decoupled from the kernel and from session:
// it works on its own Instance projection, so RenderFleet/InjectionPrompt
// are unit-testable without tmux, without a real LLM, without even the
// kernel.
package orchestrator

import (
	"fmt"
	"github.com/yro7/boulez/config"
	"os"
	"path/filepath"
	"strings"
)

// ContextFileName is the name of the context file written into the
// orchestrator's control dir. The agent reads it from its cwd (the control
// dir is the headless worktree's working directory).
const ContextFileName = "ORCHESTRATOR.md"

// MCPConfigFileName is the name of the MCP client config file written into
// the orchestrator's control dir. A conductor agent that supports MCP
// (Claude Code, Codex) discovers it from its cwd and launches
// `boulez mcp serve --as <id>` as a subprocess, binding the fleet tools.
const MCPConfigFileName = ".mcp.json"

// ControlDir returns the orchestrator's control directory
// (~/.boulez/orchestrators/<id>/). It is the cwd of the orchestrator's tmux
// session and where ORCHESTRATOR.md + plan.json live.
func ControlDir(id string) (string, error) {
	orchDir, err := config.OrchestratorsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(orchDir, id), nil
}

// WriteControlFiles writes ORCHESTRATOR.md and .mcp.json into the
// orchestrator's control dir. Safe to call repeatedly (on every boulez
// restart): both files are idempotent (same content per id), so the agent
// always sees the up-to-date tool surface even across boulez version
// upgrades. The control dir is created by the headless worktree at instance
// start; this function ensures it exists too (defensive).
//
// This is the responsibility of the spawn path's control-dir setup, not of
// any particular consumer: the TUI calls it today, `boulez ctl` or an MCP
// tool may call it tomorrow. The caller does not own the file contents.
func WriteControlFiles(id string) error {
	dir, err := ControlDir(id)
	if err != nil {
		return fmt.Errorf("resolve control dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create control dir: %w", err)
	}
	ctxPath := filepath.Join(dir, ContextFileName)
	if err := os.WriteFile(ctxPath, []byte(ContextContent(id)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", ContextFileName, err)
	}
	mcpPath := filepath.Join(dir, MCPConfigFileName)
	if err := os.WriteFile(mcpPath, []byte(MCPConfigContent(id)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", MCPConfigFileName, err)
	}
	return nil
}

// ContextContent builds the body of ORCHESTRATOR.md. It documents:
//   - the orchestrator's role and its own instance ID (for context only —
//     it does not need to pass the id to tools; the MCP server is already
//     authenticated as this instance),
//   - that the fleet tools are exposed via the `boulez` MCP server (discovered
//     automatically from .mcp.json in the cwd) — NOT a CLI to shell out to,
//   - the structured error codes it should branch on,
//   - a reminder that the fleet snapshot was injected once and that it can
//     re-fetch state with the `list_instances` tool.
//
// The full tool surface (names, parameters, descriptions) lives in the MCP
// schemas, which the conductor discovers via the standard handshake — this
// file does not duplicate it. It only carries role + behavior + error codes.
func ContextContent(id string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# boulez orchestrator\n\n")
	fmt.Fprintf(&b, "You are the **boulez global orchestrator**. You supervise a fleet of\n")
	fmt.Fprintf(&b, "worker instances (coding agents in isolated git worktrees): you spawn\n")
	fmt.Fprintf(&b, "them, give them tasks, observe their progress, and merge their branches.\n\n")

	fmt.Fprintf(&b, "## Your identity\n\n")
	fmt.Fprintf(&b, "Your instance ID is:\n\n")
	fmt.Fprintf(&b, "```\n%s\n```\n\n", id)
	fmt.Fprintf(&b, "You do not need to pass this id to your tools: the `boulez` MCP server is\n")
	fmt.Fprintf(&b, "already authenticated as this instance, so every tool call you make is\n")
	fmt.Fprintf(&b, "attributed to your plan (this is how boulez records what you spawned/merged,\n")
	fmt.Fprintf(&b, "for resumability).\n\n")

	fmt.Fprintf(&b, "## Your tools\n\n")
	fmt.Fprintf(&b, "Your fleet tools are exposed via the `boulez` **MCP server**, discovered\n")
	fmt.Fprintf(&b, "automatically from the `.mcp.json` in your working directory. Call them\n")
	fmt.Fprintf(&b, "directly as tools (list_instances, get_instance, spawn_worker, send_prompt,\n")
	fmt.Fprintf(&b, "pause, resume, kill, merge). Do NOT shell out to any CLI.\n\n")
	fmt.Fprintf(&b, "Tool calls return structured JSON. Errors carry a stable `code` field —\n")
	fmt.Fprintf(&b, "branch on `code`, never parse the message.\n\n")

	fmt.Fprintf(&b, "### Error codes\n\n")
	fmt.Fprintf(&b, "| code | meaning |\n")
	fmt.Fprintf(&b, "|---|---|\n")
	fmt.Fprintf(&b, "| `UNKNOWN_INSTANCE` | the given instance ID does not exist |\n")
	fmt.Fprintf(&b, "| `WORKER_CANNOT_SPAWN` | a worker tried to spawn (topology is two-level) |\n")
	fmt.Fprintf(&b, "| `NESTED_ORCHESTRATOR` | an orchestrator tried to spawn an orchestrator |\n")
	fmt.Fprintf(&b, "| `PROTECTED_BRANCH` | merge target is protected (main/master/host-current) |\n")
	fmt.Fprintf(&b, "| `BRANCH_NOT_FOUND` | `branch_must_exist` set but branch absent |\n")
	fmt.Fprintf(&b, "| `INTERNAL` | unexpected server error |\n\n")

	fmt.Fprintf(&b, "## Fleet state\n\n")
	fmt.Fprintf(&b, "A snapshot of the fleet was injected into your conversation **once**, at\n")
	fmt.Fprintf(&b, "startup. It is now stale. To see the current state, call the `list_instances`\n")
	fmt.Fprintf(&b, "tool.\n\n")
	fmt.Fprintf(&b, "You are supervised, not autonomous. After refreshing the fleet state above,\n")
	fmt.Fprintf(&b, "STOP and wait for an explicit task (a human attaching to your pane, or a\n")
	fmt.Fprintf(&b, "`send_prompt` tool call addressed to your id). Do NOT spawn, merge, or send\n")
	fmt.Fprintf(&b, "prompts to other instances on your own initiative. Execute the one task you\n")
	fmt.Fprintf(&b, "are given, then stop and wait again. Do not loop looking for more work to do.\n")

	return b.String()
}

// MCPConfigContent builds the body of .mcp.json: a standard MCP client config
// that tells a conductor agent (Claude Code, Codex) to launch the boulez MCP
// server as a subprocess, authenticated as this orchestrator instance. The
// conductor discovers the fleet tools via the standard MCP handshake.
func MCPConfigContent(id string) string {
	return fmt.Sprintf(`{
  "mcpServers": {
    "boulez": {
      "command": "boulez",
      "args": ["mcp", "serve", "--as", %q]
    }
  }
}
`, id)
}
