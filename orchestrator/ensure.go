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
	// IsAlive reports whether the instance's tmux session is currently alive.
	// A dead orchestrator (e.g. the user pressed Ctrl+D in its pane, killing the
	// agent and the tmux session) must be detected so Ensure can respawn it
	// rather than treating the stale record as the live instance 0.
	IsAlive(id string) bool
	// Kill removes an instance from the fleet entirely (stops it and drops its
	// record). Used by Ensure to evict a dead orchestrator before respawning a
	// fresh one, so the fleet never carries two orchestrator records (one dead,
	// one live) for the stable "instance 0" slot.
	Kill(id string) error
}

// Ensure idempotently guarantees a LIVE global orchestrator exists. It is
// the STARTUP entrypoint: on the live path it also refreshes ORCHESTRATOR.md
// so the docs stay current across cs2 upgrades. Returns the orchestrator's
// instance ID (existing or newly spawned).
//
// For the cheap periodic liveness probe (daemon poll loop), use EnsureLive
// instead — it skips the context-file rewrite on the live path so it does no
// disk I/O when instance 0 is healthy.
//
// Three cases:
//
//  1. A live orchestrator exists: refresh ORCHESTRATOR.md and do NOT
//     re-inject. Its tmux session survived and its conversation already has
//     the context.
//
//  2. An orchestrator record exists but its tmux session is dead (the user
//     pressed Ctrl+D in its pane, killing the agent and the session; or the
//     process crashed): evict the dead record, then spawn a fresh one. This
//     is the self-heal path — without it, closing the orchestrator once would
//     leave a dead record that every subsequent cs2 open would mistake for
//     "instance 0 already exists", so the panel could never be reopened short
//     of wiping storage (the "won't open again until I restart" symptom).
//     Conversation is lost on a respawn, same as the orphan-reclaim path in
//     SpawnOrchestrator; that is the accepted trade-off for an always-on
//     instance 0.
//
//  3. No orchestrator record at all: spawn the first one, write the context
//     file, and inject the fleet snapshot ONCE. The orchestrator is then
//     supervised — it waits for an explicit task and executes only that.
//
// Any extra dead orchestrator records (an anomaly — there should be exactly
// one) are evicted too, so the fleet never accumulates stale slots.
func Ensure(api API, program string) (string, error) {
	id, spawned, err := ensureCore(api, program, true /* refreshContextOnLive */)
	if err != nil {
		return id, err
	}
	if spawned {
		return id, nil // spawnFresh already wrote the context file.
	}
	// Live path: refresh the context file so docs stay current across upgrades.
	if err := WriteContextFile(id); err != nil {
		return id, err
	}
	return id, nil
}

// EnsureLive is the cheap periodic probe: it guarantees a live orchestrator
// exists but does NOT rewrite ORCHESTRATOR.md when one is already healthy
// (so the daemon's poll loop can call it every tick without disk churn). On
// the dead/missing path it respawns (which does write the context file, once,
// via spawnFresh). Returns the live orchestrator's ID.
//
// This is what makes instance 0 "always on": the daemon is long-lived (it
// keeps running after the TUI closes), so even while cs2 is closed the poll
// loop respawns a killed orchestrator within one tick. The next cs2 open
// always sees a live instance 0 — no more "closed it, won't reopen until I
// restart".
func EnsureLive(api API, program string) (string, error) {
	id, _, err := ensureCore(api, program, false /* refreshContextOnLive */)
	return id, err
}

// ensureCore is the shared engine. It evicts dead orchestrator records, finds
// the first live one, and respawns (via spawnFresh) if none is live. When
// refreshContextOnLive is true, the live path also rewrites ORCHESTRATOR.md
// (the startup behaviour). Returns (liveID, spawned, err) where spawned is
// true iff a fresh orchestrator was created this call (in which case the
// context file was already written by spawnFresh).
func ensureCore(api API, program string, refreshContextOnLive bool) (string, bool, error) {
	orchs := api.ListOrchestrators()

	// Evict dead orchestrator records and find the first live one.
	var liveID string
	for _, o := range orchs {
		if api.IsAlive(o.ID) {
			if liveID == "" {
				liveID = o.ID
			}
			continue
		}
		// Stale record (dead tmux). Drop it so the respawn does not leave a
		// second orchestrator slot competing for "instance 0".
		if err := api.Kill(o.ID); err != nil {
			// Non-fatal: the record may linger, but we proceed. The fresh
			// spawn reclaims the stable tmux session name regardless.
			return "", false, fmt.Errorf("evict dead orchestrator %s: %w", o.ID, err)
		}
	}

	if liveID != "" {
		return liveID, false, nil
	}

	// No live orchestrator — spawn a fresh one.
	id, err := spawnFresh(api, program)
	if err != nil {
		return id, false, err
	}
	return id, true, nil
}

// spawnFresh creates the first/live orchestrator: spawn it, write the context
// file, and inject the fleet snapshot once. Shared by first-creation and the
// respawn-after-death path.
func spawnFresh(api API, program string) (string, error) {
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
