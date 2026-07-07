package orchestrator

import (
	"fmt"
	"strings"
)

// Instance is the orchestrator package's view of a fleet member. It is a
// decoupled projection: the orchestrator package does not import the kernel
// (nor session), so RenderFleet/InjectionPrompt are unit-testable without the
// kernel (and without tmux). The TUI adapts its []*session.Instance to this
// type at the seam (app.toOrchestratorFleet).
type Instance struct {
	ID      string
	Kind    string // "worker" | "orchestrator"
	Status  string // "running" | "ready" | "loading" | "paused"
	Title   string
	Repo    string
	Branch  string
	Program string
	Host    string
}

// RenderFleet renders a fleet snapshot as compact text for injection into the
// orchestrator's pane. One line per instance, ordered as given. The format is
// deliberately line-oriented (not a JSON blob) so it reads naturally as a
// status report in the agent's conversation.
func RenderFleet(instances []Instance) string {
	if len(instances) == 0 {
		return "(no instances in the fleet)"
	}
	var b strings.Builder
	b.WriteString("Current fleet:\n")
	for _, in := range instances {
		fmt.Fprintf(&b, "  - id=%s kind=%s status=%s title=%q repo=%q branch=%q program=%q host=%q\n",
			in.ID, in.Kind, in.Status, in.Title, in.Repo, in.Branch, in.Program, in.Host)
	}
	return b.String()
}

// InjectionPrompt builds the one-time prompt pushed into the orchestrator's
// pane at first creation. It carries the fleet snapshot and points the agent
// at ORCHESTRATOR.md (its cwd) for the role doc, and at the `boulez` MCP
// server (discovered via .mcp.json in its cwd) for the tool surface.
//
// This is injected EXACTLY ONCE (when the orchestrator is freshly created).
// On boulez restart the orchestrator's tmux session is restored and its
// conversation survives, so re-injecting would duplicate context. The agent
// re-fetches fresh state itself via the `list_instances` tool.
func InjectionPrompt(fleetText string) string {
	return fmt.Sprintf(`You are the boulez global orchestrator. A full description of your role is in ./ORCHESTRATOR.md in your working directory — read it now.

Your fleet tools are exposed via the `+"`boulez`"+` MCP server, discovered from the .mcp.json in your working directory. Call them directly as tools (list_instances, get_instance, spawn_worker, send_prompt, pause, resume, kill, merge). Do NOT shell out to any CLI.

Here is the current fleet state, injected once at startup. It is already stale; call the `+"`list_instances`"+` tool to refresh it whenever you need the current state.

%s

You are supervised, not autonomous. Do the following, in order:
1. Read ./ORCHESTRATOR.md.
2. Refresh the fleet state with the `+"`list_instances`"+` tool.
3. STOP and wait for an explicit task. A task comes either from a human attaching to your pane, or from a `+"`send_prompt`"+` tool call addressed to your id.

Do NOT spawn, merge, or send prompts to other instances on your own initiative. Wait for an explicit instruction, execute that one task, then stop and wait again. Do not loop looking for more work to do.`, fleetText)
}
