package daemon

import (
	"claude-squad/host"
	"claude-squad/kernel"
	"claude-squad/orchestrator"
	"claude-squad/session"
)

// orchestratorAPI adapts *kernel.Kernel to the orchestrator.API interface.
// It is the bridge between the bootstrap layer (agent-agnostic, testable) and
// the kernel (the single writer). The daemon constructs it after wiring the
// kernel and passes it to orchestrator.Ensure so instance 0 is guaranteed at
// startup — through the kernel, so the plan/storage stay consistent.
//
// Why a separate type and not methods on *Kernel: the kernel is
// consumer-agnostic (it must not know "there is always one orchestrator").
// The "ensure global orchestrator" policy lives in the daemon, layered above
// the kernel, exactly where the handoff says it should.
type orchestratorAPI struct {
	k       *kernel.Kernel
	program string // the default agent program (cfg.GetProgram())
}

// toInstance projects a kernel.InstanceSummary to the orchestrator package's
// decoupled Instance type. The orchestrator package does not import the
// kernel, so the projection happens here at the seam.
func toInstance(s kernel.InstanceSummary) orchestrator.Instance {
	return orchestrator.Instance{
		ID:      s.ID,
		Kind:    s.Kind.String(),
		Status:  s.Status.String(),
		Title:   s.Title,
		Repo:    s.Repo,
		Branch:  s.Branch,
		Program: s.Program,
		Host:    s.Host,
	}
}

func (a *orchestratorAPI) ListInstances() []orchestrator.Instance {
	summaries := a.k.ListInstances(kernel.ListFilter{})
	out := make([]orchestrator.Instance, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, toInstance(s))
	}
	return out
}

func (a *orchestratorAPI) ListOrchestrators() []orchestrator.Instance {
	summaries := a.k.ListInstances(kernel.FilterByKind(session.KindOrchestrator))
	out := make([]orchestrator.Instance, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, toInstance(s))
	}
	return out
}

// SpawnOrchestrator creates and starts a new orchestrator via the kernel.
// It spawns top-level (no caller): instance 0 is a policy the daemon owns,
// not attributed to another orchestrator. The orchestrator runs the daemon's
// configured default program (Pi by user choice, but any terminal agent).
func (a *orchestratorAPI) SpawnOrchestrator(program string) (string, error) {
	return a.k.Spawn(kernel.CallerContext{}, kernel.SpawnOptions{
		// An orchestrator has no repo (headless worktree). Repo is left empty;
		// app.Spawn relaxes the repo requirement for KindOrchestrator.
		Kind:    session.KindOrchestrator,
		Program: program,
		Title:   "orchestrator",
		Host:    host.Local,
	})
}

func (a *orchestratorAPI) SendPrompt(id, prompt string) error {
	return a.k.SendPrompt(id, prompt)
}

// Compile-time check that orchestratorAPI satisfies the bootstrap interface.
var _ orchestrator.API = (*orchestratorAPI)(nil)
