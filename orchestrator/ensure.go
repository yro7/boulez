package orchestrator

import "fmt"

// API is the control-authority surface the bootstrap needs. It is a minimal
// slice of the kernel's syscall surface, expressed in the orchestrator
// package's own types so this package does not import the kernel. Production
// wires it over *kernel.Kernel (via a thin adapter in the daemon); tests inject
// a fake. This is the deep-module move: a tiny interface hiding "ensure a
// global orchestrator exists", testable without tmux, without an LLM,
// without the kernel.
type API interface {
	// ListInstances returns the whole fleet.
	ListInstances() []Instance
	// ListOrchestrators returns only orchestrator instances.
	ListOrchestrators() []Instance
	// SpawnOrchestrator creates and starts a new orchestrator instance running
	// the given agent program (e.g. "pi", "claude"). Returns the new ID.
	SpawnOrchestrator(program string) (string, error)
	// SendPrompt pushes text into an instance's tmux pane.
	SendPrompt(id, prompt string) error
}

// Ensure idempotently guarantees a global orchestrator exists. It returns the
// orchestrator's instance ID (existing or newly spawned).
//
// First creation (no orchestrator in the fleet): spawn one, write the context
// file (ORCHESTRATOR.md), and inject the fleet snapshot into its pane ONCE.
// The orchestrator is then autonomous — it re-fetches state and drives the
// fleet via `cs2 ctl` at its own pace.
//
// Restart (an orchestrator already exists): refresh ORCHESTRATOR.md only (so
// the documentation stays current across cs2 upgrades) and do NOT re-inject.
// The orchestrator's tmux session survives a restart and its conversation
// already has the context; re-injecting would duplicate it.
func Ensure(api API, program string) (string, error) {
	orchs := api.ListOrchestrators()
	if len(orchs) > 0 {
		// An orchestrator already exists (restored from a previous session).
		// Refresh the context file; do not re-inject the prompt.
		if err := WriteContextFile(orchs[0].ID); err != nil {
			return orchs[0].ID, err
		}
		return orchs[0].ID, nil
	}

	id, err := api.SpawnOrchestrator(program)
	if err != nil {
		return "", fmt.Errorf("spawn orchestrator: %w", err)
	}
	if err := WriteContextFile(id); err != nil {
		return id, fmt.Errorf("write context: %w", err)
	}
	fleet := api.ListInstances()
	if err := api.SendPrompt(id, InjectionPrompt(RenderFleet(fleet))); err != nil {
		// The orchestrator is started; a prompt failure is not fatal to the
		// ensure itself — the instance exists and ORCHESTRATOR.md is written,
		// so the agent can recover. Surface it but do not undo the spawn.
		return id, fmt.Errorf("inject fleet snapshot: %w", err)
	}
	return id, nil
}
