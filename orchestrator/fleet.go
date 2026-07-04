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
// at ORCHESTRATOR.md (its cwd) for the full tool documentation.
//
// This is injected EXACTLY ONCE (when the orchestrator is freshly created).
// On cs2 restart the orchestrator's tmux session is restored and its
// conversation survives, so re-injecting would duplicate context. The agent
// re-fetches fresh state itself via `cs2 ctl list_instances`.
func InjectionPrompt(fleetText string) string {
	return fmt.Sprintf(`You are the cs2 global orchestrator. A full description of your role and your tools is in ./ORCHESTRATOR.md in your working directory — read it now.

Here is the current fleet state, injected once at startup. It is already stale; call `+"`cs2 ctl list_instances`"+` to refresh it whenever you need the current state.

%s

You are supervised, not autonomous. Do the following, in order:
1. Read ./ORCHESTRATOR.md.
2. Refresh the fleet state with `+"`cs2 ctl list_instances`"+`.
3. STOP and wait for an explicit task. A task comes either from a human attaching to your pane, or from a `+"`cs2 ctl send_prompt --id <your-id>`"+`.

Do NOT spawn, merge, or send prompts to other instances on your own initiative. Wait for an explicit instruction, execute that one task, then stop and wait again. Do not loop looking for more work to do.
When you do act on an instruction, use `+"`cs2 ctl as <your-id> <syscall>`"+` for spawn/merge so your actions are recorded on your plan.`, fleetText)
}
