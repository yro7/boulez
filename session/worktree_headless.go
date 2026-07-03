package session

import (
	"claude-squad/config"
	"claude-squad/session/fs"
	"claude-squad/session/git"
	"os"
	"path/filepath"
)

// headlessWorktree is the Worktree implementation for an Orchestrator
// instance. An orchestrator supervises the fleet — it does not edit code in
// a supervised repo, so it has no git worktree, no branch, no base commit,
// and no diff. Every git operation is a no-op.
//
// What it DOES have is a control directory (~/.cs2/orchestrators/<id>/):
// the working dir the orchestrator's tmux session runs in, and where its
// plan.json lives (persisted by the kernel, not here). Setup creates it;
// the no-op cleanups deliberately never delete it (plan data outlives a
// tmux detach).
//
// This type exists so that Instance never branches on Kind outside the
// single factory (buildWorktree / restoreWorktree) that picks real vs
// headless. All the Worker-only logic in Pause/Resume/Kill (commit, remove,
// prune) calls through the Worktree interface and becomes a safe no-op here.
type headlessWorktree struct {
	id         string
	controlDir string
	fsys       fs.FS // injected so tests can swap the FS; defaults to local.
}

// newHeadlessWorktree builds a headless worktree for the given instance ID.
// The control dir is derived from the cs2 config dir + id, so it is stable
// across restarts (the same ID always maps to the same control dir).
func newHeadlessWorktree(id string) (Worktree, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return nil, err
	}
	controlDir := filepath.Join(configDir, "orchestrators", id)
	return &headlessWorktree{
		id:         id,
		controlDir: controlDir,
		fsys:       fs.LocalFS{},
	}, nil
}

// Setup creates the control directory. This is the only operation with a
// real side effect — the orchestrator's tmux session needs a working dir.
func (h *headlessWorktree) Setup() error {
	if err := h.fsys.MkdirAll(h.controlDir, 0o755); err != nil {
		return err
	}
	return nil
}

// Cleanup is a no-op: the control dir holds plan data and outlives a tmux
// detach. The kernel owns its lifecycle, not the worktree.
func (h *headlessWorktree) Cleanup() error { return nil }

// Remove is a no-op (see Cleanup).
func (h *headlessWorktree) Remove() error { return nil }

// Prune is a no-op: there are no git worktrees to prune.
func (h *headlessWorktree) Prune() error { return nil }

// RemoveWorktreeDir is a no-op: the control dir is not a worktree dir and is
// not removed on pause (plan data persists across pause/resume).
func (h *headlessWorktree) RemoveWorktreeDir() error { return nil }

func (h *headlessWorktree) IsDirty() (bool, error)            { return false, nil }
func (h *headlessWorktree) IsValidWorktree() (bool, error)    { return true, nil }
func (h *headlessWorktree) IsBranchCheckedOut() (bool, error) { return false, nil }
func (h *headlessWorktree) IsExistingBranch() bool            { return false }

// WorktreeDirExists reports whether the control dir exists. Used by Pause to
// decide whether to attempt Remove; returning the truth keeps Pause's logic
// honest without special-casing the orchestrator.
func (h *headlessWorktree) WorktreeDirExists() bool {
	_, err := os.Stat(h.controlDir)
	return err == nil
}

// CommitChanges is a no-op: an orchestrator has nothing to commit (it does
// not edit code in a supervised repo). The control dir is not a git repo.
func (h *headlessWorktree) CommitChanges(string) error { return nil }

// PushChanges is a no-op: no branch, no remote.
func (h *headlessWorktree) PushChanges(string, bool) error { return nil }

// Diff / DiffNumstat return nil: no git repo, no diff. Callers already
// nil-check the result.
func (h *headlessWorktree) Diff() *git.DiffStats        { return nil }
func (h *headlessWorktree) DiffNumstat() *git.DiffStats  { return nil }

func (h *headlessWorktree) GetWorktreePath() string { return h.controlDir }
func (h *headlessWorktree) GetBranchName() string   { return "" }
func (h *headlessWorktree) GetRepoPath() string     { return "" }
func (h *headlessWorktree) GetRepoName() string     { return "" }
func (h *headlessWorktree) GetBaseCommitSHA() string {
	return ""
}

// ensure headlessWorktree satisfies Worktree at compile time.
var _ Worktree = (*headlessWorktree)(nil)

// withHeadlessFS is a test seam: swap the FS used by a headlessWorktree.
// Not exported; tests in this package may construct via newHeadlessWorktree
// and patch the field directly when needed.
func (h *headlessWorktree) withFS(fsys fs.FS) *headlessWorktree {
	h.fsys = fsys
	return h
}
