package daemon

import (
	"claude-squad/cmd"
	"claude-squad/host"
	"claude-squad/kernel"
	"claude-squad/log"
	"claude-squad/orchestrator"
	"claude-squad/session"
	"claude-squad/session/tmux"
)

// orchestratorSessionTitle is the stable title used for the global
// orchestrator instance. Because the tmux session name is derived from the
// title, a stable title yields a stable session name
// (claudesquad_orchestrator). This is what makes the orphan-reclaim check in
// SpawnOrchestrator reliable: the session name is predictable, not a
// timestamp-derived collision-avoidance suffix.
const orchestratorSessionTitle = "orchestrator"

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
//
// Before spawning it reclaims any orphaned tmux session left by a previous
// run that crashed before the kernel could persist the orchestrator (the
// "tmux session already exists: claudesquad_orchestrator" loop seen in
// dogfooding). Ensure has already determined NO orchestrator instance is in
// the fleet, so a surviving session with this name is by definition an orphan
// — kill it so the fresh spawn does not collide. Conversation is lost only in
// this broken state; the normal restart path preserves conversation (Ensure
// takes the restart branch and the kernel reattaches to the live session).
func (a *orchestratorAPI) SpawnOrchestrator(program string) (string, error) {
	reclaimOrphanedOrchestratorSession()
	return a.k.Spawn(kernel.CallerContext{}, kernel.SpawnOptions{
		// An orchestrator has no repo (headless worktree). Repo is left empty;
		// app.Spawn relaxes the repo requirement for KindOrchestrator.
		Kind:    session.KindOrchestrator,
		Program: program,
		Title:   orchestratorSessionTitle,
		Host:    host.Local,
	})
}

// reclaimOrphanedOrchestratorSession kills a leftover claudesquad_orchestrator
// tmux session if one exists. See SpawnOrchestrator for the rationale.
func reclaimOrphanedOrchestratorSession() {
	name := tmux.SessionName(orchestratorSessionTitle)
	exec := cmd.MakeExecutor()
	if !tmux.SessionExists(exec, name) {
		return
	}
	log.WarningLog.Printf("orchestrator: reclaiming orphaned tmux session %s", name)
	if err := tmux.KillSession(exec, name); err != nil {
		// Non-fatal: the spawn may still collide, but we tried. Surface it so
		// the failure is diagnosable rather than a silent skip.
		log.WarningLog.Printf("orchestrator: could not kill orphaned session %s: %v", name, err)
	}
}

func (a *orchestratorAPI) SendPrompt(id, prompt string) error {
	return a.k.SendPrompt(id, prompt)
}

// IsAlive reports whether the orchestrator's tmux session is currently alive.
// Ensure uses this to distinguish a live instance 0 from a dead record left
// behind when the user killed the agent (Ctrl+D) — the symptom that motivated
// this check. Delegated to the kernel, which holds the live *session.Instance;
// TmuxAlive is a read-only tmux probe (no mutation).
func (a *orchestratorAPI) IsAlive(id string) bool {
	inst, err := a.k.InstanceByID(id)
	if err != nil {
		// Unknown ID — treat as not alive so Ensure evicts the stale record
		// (rather than pinning on a record that points at nothing).
		return false
	}
	return inst.TmuxAlive()
}

// Kill removes an instance from the fleet entirely. Ensure uses it to evict a
// dead orchestrator record before respawning a fresh one, so the fleet never
// carries two orchestrator slots (one dead, one live) competing for instance 0.
// This goes through the kernel (the single writer) so the eviction is
// persisted like any other mutation.
func (a *orchestratorAPI) Kill(id string) error {
	return a.k.Kill(id)
}

// Compile-time check that orchestratorAPI satisfies the bootstrap interface.
var _ orchestrator.API = (*orchestratorAPI)(nil)
