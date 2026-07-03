package session

import (
	"fmt"

	"claude-squad/session/git"
)

// LandCaller is the seam LandInstance uses to perform the kernel Land
// syscall. The real *kernel.Kernel satisfies it; tests inject a fake. This
// keeps the helper decoupled from the kernel package (no import cycle:
// session does not depend on kernel) and testable in isolation.
//
// The caller identity is constructed by LandInstance as a top-level
// CallerContext (empty) — LandInstance is invoked only from the TUI / `cs2
// ctl`, which are top-level callers. The concrete kernel enforces that the
// session reaching it is top-level.
type LandCaller interface {
	// Land merges sourceBranch into targetBranch of repoPath. May target a
	// trunk (main/master). Returns the merge outcome; on conflict the repo
	// is left in the merging state.
	Land(repoPath, targetBranch, sourceBranch string, strategy git.Strategy) (git.MergeResult, error)
}

// LandResult is the outcome of LandInstance.
type LandResult struct {
	// Pushed is true if a commit+push happened (the worktree was dirty).
	// A clean worktree skips the push entirely.
	Pushed bool
	// Merge is the outcome of the kernel Land. On conflict it carries the
	// conflicted files and the repo is left for resolution.
	Merge git.MergeResult
}

// LandInstance commits+pushes the instance's worktree (if dirty) then lands
// its branch into targetBranch via the kernel. commitMsg is used only if a
// commit is needed. open=false on push (no browser during a land). On merge
// conflict, the repo is left in merging state and the conflict list is
// returned; the instance is untouched (its worktree is independent of the
// target repo's working tree, so a conflict on main does not corrupt the
// agent's branch).
//
// The instance must be on a real git worktree (a Worker); a headless
// (orchestrator) worktree has no branch and no repo path, so LandInstance
// returns an error in that case rather than attempting a no-op land.
func LandInstance(inst *Instance, kernelLand LandCaller, targetBranch, commitMsg string) (LandResult, error) {
	wt, err := inst.GetGitWorktree()
	if err != nil {
		return LandResult{}, fmt.Errorf("land: get worktree: %w", err)
	}

	repoPath := wt.GetRepoPath()
	branch := wt.GetBranchName()
	if repoPath == "" || branch == "" {
		// Headless worktree (orchestrator): nothing to land.
		return LandResult{}, fmt.Errorf("land: instance %q has no git worktree to land (headless/orchestrator)", inst.Title)
	}

	var res LandResult

	// Commit+push if there is anything to land. A clean worktree (already
	// pushed) skips straight to the merge.
	if dirty, err := wt.IsDirty(); err != nil {
		return LandResult{}, fmt.Errorf("land: check dirty: %w", err)
	} else if dirty {
		if err := wt.PushChanges(commitMsg, false); err != nil {
			return LandResult{}, fmt.Errorf("land: push: %w", err)
		}
		res.Pushed = true
	}

	merge, err := kernelLand.Land(repoPath, targetBranch, branch, git.StrategyDefault)
	res.Merge = merge
	if err != nil {
		// A conflict is returned as a non-nil error by the Merger (git merge
		// exits non-zero); the result still carries the conflict list. We
		// surface the error so the caller can distinguish success from
		// conflict, but the result is populated.
		return res, fmt.Errorf("land: merge: %w", err)
	}
	return res, nil
}
