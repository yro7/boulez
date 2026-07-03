package session

import (
	"errors"
	"testing"

	"claude-squad/session/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")


// fakeLandWorktree is a test Worktree that scripts dirty/push behavior. It
// implements just the surface LandInstance touches (IsDirty, PushChanges,
// GetRepoPath, GetBranchName); the other interface methods panic if called,
// to surface unintended coupling.
type fakeLandWorktree struct {
	dirty     bool
	pushErr   error
	pushed    bool
	pushCalls int
	repoPath  string
	branch    string
}

func (f *fakeLandWorktree) IsDirty() (bool, error)         { return f.dirty, nil }
func (f *fakeLandWorktree) PushChanges(msg string, open bool) error {
	f.pushCalls++
	f.pushed = true
	return f.pushErr
}
func (f *fakeLandWorktree) GetRepoPath() string  { return f.repoPath }
func (f *fakeLandWorktree) GetBranchName() string { return f.branch }

// unused methods — panic to surface coupling if LandInstance starts calling them.
func (f *fakeLandWorktree) Setup() error                { panic("unexpected") }
func (f *fakeLandWorktree) Cleanup() error             { panic("unexpected") }
func (f *fakeLandWorktree) Remove() error               { panic("unexpected") }
func (f *fakeLandWorktree) Prune() error                { panic("unexpected") }
func (f *fakeLandWorktree) IsValidWorktree() (bool, error) { panic("unexpected") }
func (f *fakeLandWorktree) WorktreeDirExists() bool     { panic("unexpected") }
func (f *fakeLandWorktree) IsBranchCheckedOut() (bool, error) { panic("unexpected") }
func (f *fakeLandWorktree) IsExistingBranch() bool     { panic("unexpected") }
func (f *fakeLandWorktree) RemoveWorktreeDir() error    { panic("unexpected") }
func (f *fakeLandWorktree) CommitChanges(string) error { panic("unexpected") }
func (f *fakeLandWorktree) Diff() *git.DiffStats        { panic("unexpected") }
func (f *fakeLandWorktree) DiffNumstat() *git.DiffStats { panic("unexpected") }
func (f *fakeLandWorktree) GetWorktreePath() string     { panic("unexpected") }
func (f *fakeLandWorktree) GetRepoName() string          { panic("unexpected") }
func (f *fakeLandWorktree) GetBaseCommitSHA() string     { panic("unexpected") }

// fakeLandCaller is a test LandCaller that records the call and scripts the
// merge result / error.
type fakeLandCaller struct {
	result  git.MergeResult
	err     error
	called  bool
	repo    string
	target  string
	source  string
	strat   git.Strategy
}

func (f *fakeLandCaller) Land(repoPath, targetBranch, sourceBranch string, strategy git.Strategy) (git.MergeResult, error) {
	f.called = true
	f.repo = repoPath
	f.target = targetBranch
	f.source = sourceBranch
	f.strat = strategy
	return f.result, f.err
}

// newLandInstance builds a Worker Instance with a fake worktree installed
// directly (bypassing Start/tmux). LandInstance only needs GetGitWorktree +
// the worktree methods above.
func newLandInstance(t *testing.T, wt Worktree) *Instance {
	t.Helper()
	inst, err := NewInstance(InstanceOptions{Title: "w", Path: "/repo", Kind: KindWorker})
	require.NoError(t, err)
	inst.gitWorktree = wt
	inst.started = true
	return inst
}

// TestLandInstance_DirtyCommitsAndPushes proves the dirty path: a dirty
// worktree is pushed (PushChanges with open=false) before Land is called,
// and Land receives the instance's branch + repo path.
func TestLandInstance_DirtyCommitsAndPushes(t *testing.T) {
	wt := &fakeLandWorktree{dirty: true, repoPath: "/repo", branch: "feat"}
	caller := &fakeLandCaller{result: git.MergeResult{Status: git.MergeMerged}}
	inst := newLandInstance(t, wt)

	res, err := LandInstance(inst, caller, "main", "msg")
	require.NoError(t, err)
	assert.True(t, res.Pushed, "dirty worktree must be pushed")
	assert.Equal(t, 1, wt.pushCalls, "PushChanges called once")

	assert.True(t, caller.called, "Land was called")
	assert.Equal(t, "/repo", caller.repo)
	assert.Equal(t, "main", caller.target)
	assert.Equal(t, "feat", caller.source)
	assert.Equal(t, git.StrategyDefault, caller.strat)
	assert.Equal(t, git.MergeMerged, res.Merge.Status)
}

// TestLandInstance_CleanSkipsPush proves a clean worktree skips the push and
// goes straight to Land. Pushed=false, Land still called with the branch.
func TestLandInstance_CleanSkipsPush(t *testing.T) {
	wt := &fakeLandWorktree{dirty: false, repoPath: "/repo", branch: "feat"}
	caller := &fakeLandCaller{result: git.MergeResult{Status: git.MergeMerged}}
	inst := newLandInstance(t, wt)

	res, err := LandInstance(inst, caller, "main", "msg")
	require.NoError(t, err)
	assert.False(t, res.Pushed, "clean worktree must not push")
	assert.Equal(t, 0, wt.pushCalls, "PushChanges not called")
	assert.True(t, caller.called, "Land is still called")
	assert.Equal(t, "feat", caller.source)
}

// TestLandInstance_ConflictPropagated proves a conflict is returned as a
// populated MergeResult, and the worktree is left intact (the conflict is on
// the target repo, not the instance's worktree). This variant covers a
// merger that reports a conflict result WITHOUT an error.
func TestLandInstance_ConflictPropagated(t *testing.T) {
	wt := &fakeLandWorktree{dirty: true, repoPath: "/repo", branch: "feat"}
	caller := &fakeLandCaller{
		result: git.MergeResult{
			Status:    git.MergeConflict,
			Conflicts: []git.Conflict{{File: "a.go"}},
		},
		err: nil, // some merger impls report a conflict with err=nil
	}
	inst := newLandInstance(t, wt)

	res, err := LandInstance(inst, caller, "main", "msg")
	require.NoError(t, err, "conflict-without-error surfaces as success+conflict result")
	assert.Equal(t, git.MergeConflict, res.Merge.Status)
	require.Len(t, res.Merge.Conflicts, 1)
	assert.Equal(t, "a.go", res.Merge.Conflicts[0].File)
	assert.True(t, res.Pushed, "push still happened before the conflict")
}

// TestLandInstance_ConflictWithError proves the merger-error path: the
// helper wraps and surfaces the error while still returning the populated
// result, so the caller can render the conflict list.
func TestLandInstance_ConflictWithError(t *testing.T) {
	wt := &fakeLandWorktree{dirty: false, repoPath: "/repo", branch: "feat"}
	caller := &fakeLandCaller{
		result: git.MergeResult{Status: git.MergeConflict, Conflicts: []git.Conflict{{File: "a.go"}}},
		err:    errBoom,
	}
	inst := newLandInstance(t, wt)

	res, err := LandInstance(inst, caller, "main", "msg")
	require.Error(t, err, "merger error must surface")
	assert.Contains(t, err.Error(), "merge")
	assert.Equal(t, git.MergeConflict, res.Merge.Status)
	require.Len(t, res.Merge.Conflicts, 1)
}

// TestLandInstance_PushErrorPropagates proves a push failure aborts before
// the land (no point landing an unpushed branch).
func TestLandInstance_PushErrorPropagates(t *testing.T) {
	wt := &fakeLandWorktree{dirty: true, repoPath: "/repo", branch: "feat", pushErr: errBoom}
	caller := &fakeLandCaller{}
	inst := newLandInstance(t, wt)

	_, err := LandInstance(inst, caller, "main", "msg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "push")
	assert.False(t, caller.called, "Land must not run after a push failure")
}

// TestLandInstance_HeadlessRefused proves an orchestrator (headless worktree
// with no repo/branch) is rejected rather than no-op-landed.
func TestLandInstance_HeadlessRefused(t *testing.T) {
	wt := &fakeLandWorktree{dirty: false, repoPath: "", branch: ""} // headless
	caller := &fakeLandCaller{}
	inst := newLandInstance(t, wt)

	_, err := LandInstance(inst, caller, "main", "msg")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no git worktree")
	assert.False(t, caller.called)
}
