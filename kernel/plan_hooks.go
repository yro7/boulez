package kernel

import "claude-squad/session/git"

// recordWorkerInPlan appends a worker ID to the orchestrator's plan, creating
// the plan in Running state if it doesn't exist yet. Best-effort: callers
// ignore the error (a plan-save failure must not abort a successful spawn).
//
// This is the spawn half of the resumability substrate: every worker an
// orchestrator spawns is recorded, so on restart the orchestrator can see
// its fleet even if the instance list was lost.
func recordWorkerInPlan(orchestratorID, workerID string) error {
	p, err := LoadPlan(orchestratorID)
	if err != nil {
		// No plan yet — create one in Running state.
		p = &OrchestratorPlan{
			ID:        orchestratorID,
			WorkerIDs: []string{},
			State:     PlanRunning,
		}
	}
	// Dedup: a re-spawn after resume shouldn't double-record.
	for _, w := range p.WorkerIDs {
		if w == workerID {
			return nil
		}
	}
	p.WorkerIDs = append(p.WorkerIDs, workerID)
	if p.State == "" {
		p.State = PlanRunning
	}
	return SavePlan(p)
}

// RecordMerge transitions an orchestrator's plan to Merging (the merge is
// underway) and records the merge target. Called by the kernel's Merge when
// the caller is an orchestrator. Best-effort persistence.
func RecordMerge(orchestratorID string, target MergeTarget) error {
	if orchestratorID == "" {
		return nil // top-level ctl merge — no plan to update
	}
	p, err := LoadPlan(orchestratorID)
	if err != nil {
		p = &OrchestratorPlan{ID: orchestratorID, State: PlanMerging}
	}
	p.State = PlanMerging
	// Dedup the target.
	found := false
	for _, t := range p.MergeTargets {
		if t.Repo == target.Repo && t.Branch == target.Branch {
			found = true
			break
		}
	}
	if !found {
		p.MergeTargets = append(p.MergeTargets, target)
	}
	return SavePlan(p)
}

// recordMergeOutcome transitions the plan based on the merge result. A clean
// merge → Done (if all targets merged); a conflict → Failed (v1: the
// orchestrator must resolve before the plan can progress). Wired into Merge.
func recordMergeOutcome(orchestratorID string, res git.MergeResult) error {
	if orchestratorID == "" {
		return nil
	}
	p, err := LoadPlan(orchestratorID)
	if err != nil {
		p = &OrchestratorPlan{ID: orchestratorID}
	}
	switch res.Status {
	case git.MergeMerged:
		p.State = PlanDone
	case git.MergeConflict:
		// v1: a conflict blocks the plan. The orchestrator (Shape B) is
		// expected to spawn a resolver worker; until then the plan is Failed.
		p.State = PlanFailed
	}
	return SavePlan(p)
}
