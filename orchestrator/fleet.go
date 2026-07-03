package orchestrator

import (
	"fmt"
	"strings"
)

// Instance is the orchestrator package's view of a fleet member. It is a
// decoupled projection of kernel.InstanceSummary: the orchestrator package
// does not import the kernel, so its bootstrap logic is unit-testable without
// the kernel (and without tmux). The daemon adapts kernel.InstanceSummary to
// this type at the seam.
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

You are autonomous: spawn workers, give them tasks, observe them with `+"`get_instance`"+`, merge their branches when they are done, and resolve issues. Use `+"`cs2 ctl as <your-id> <syscall>`"+` for spawn/merge so your actions are recorded on your plan. Decide what to do and proceed.`, fleetText)
}
