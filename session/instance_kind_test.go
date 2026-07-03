package session

import (
	"os"
	"path/filepath"
	"testing"

	"claude-squad/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInstance_Kind_DefaultsToWorker proves a freshly created instance is a
// Worker (the back-compat default). Legacy persisted data also restores as
// Worker.
func TestInstance_Kind_DefaultsToWorker(t *testing.T) {
	repoPath := makeTempGitRepo(t)

	inst, err := NewInstance(InstanceOptions{Title: "t", Path: repoPath, Program: "claude"})
	require.NoError(t, err)
	assert.Equal(t, KindWorker, inst.Kind(), "new instance defaults to Worker")
}

// TestInstance_Kind_PersistsAcrossRoundTrip proves the Kind survives a
// save→load round-trip, so an orchestrator instance is still an orchestrator
// after a cs2 restart.
func TestInstance_Kind_PersistsAcrossRoundTrip(t *testing.T) {
	repoPath := makeTempGitRepo(t)

	inst, err := NewInstance(InstanceOptions{
		Title: "orch",
		Path:  repoPath,
		ID:    "orch-1",
		Kind:  KindOrchestrator,
	})
	require.NoError(t, err)
	// Simulate a paused orchestrator so FromInstanceData doesn't call Start()
	// (which needs tmux).
	inst.Status = Paused

	data := inst.ToInstanceData()
	assert.Equal(t, KindOrchestrator, data.Kind, "ToInstanceData must serialize the Kind")

	restored, err := FromInstanceData(data)
	require.NoError(t, err)
	assert.Equal(t, KindOrchestrator, restored.Kind(), "Kind must survive round-trip")
}

// TestInstance_Kind_LegacyDataRestoresAsWorker proves back-compat: an
// InstanceData persisted before the Kind field existed (zero value =
// KindWorker) restores as a Worker.
func TestInstance_Kind_LegacyDataRestoresAsWorker(t *testing.T) {
	repoPath := makeTempGitRepo(t)

	legacy := InstanceData{
		Title:   "legacy",
		Path:    repoPath,
		Status:  Paused,
		Program: "claude",
		Worktree: GitWorktreeData{
			RepoPath:      repoPath,
			WorktreePath:  filepath.Join(t.TempDir(), "wt"),
			SessionName:   "legacy",
			BranchName:    "cs2/legacy",
			BaseCommitSHA: "HEAD",
		},
		// Kind omitted → zero value KindWorker.
	}
	restored, err := FromInstanceData(legacy)
	require.NoError(t, err)
	assert.Equal(t, KindWorker, restored.Kind(), "legacy data (no Kind) must restore as Worker")
}

// TestHeadlessWorktree_NoOpsAndControlDir proves the headless worktree
// contract: every git operation is a no-op, but Setup creates a real control
// dir (the orchestrator's working dir), and GetWorktreePath returns it.
func TestHeadlessWorktree_NoOpsAndControlDir(t *testing.T) {
	// Isolate HOME so the control dir lands under a temp dir.
	tempHome := t.TempDir()
	orig, _ := os.LookupEnv("HOME")
	require.NoError(t, os.Setenv("HOME", tempHome))
	defer func() { _ = os.Setenv("HOME", orig) }()

	const id = "orch-headless-test"
	wt, err := newHeadlessWorktree(id)
	require.NoError(t, err)

	// Before Setup: control dir does not exist, path is derived from id.
	assert.False(t, wt.WorktreeDirExists(), "control dir must not exist before Setup")
	configDir, err := config.GetConfigDir()
	require.NoError(t, err)
	wantDir := filepath.Join(configDir, "orchestrators", id)
	assert.Equal(t, wantDir, wt.GetWorktreePath(), "control dir derives from id")

	// Setup creates the dir.
	require.NoError(t, wt.Setup())
	assert.True(t, wt.WorktreeDirExists(), "control dir exists after Setup")

	// All git operations are no-ops and never error.
	dirty, err := wt.IsDirty()
	assert.False(t, dirty)
	assert.NoError(t, err)

	valid, err := wt.IsValidWorktree()
	assert.True(t, valid)
	assert.NoError(t, err)

	checkedOut, err := wt.IsBranchCheckedOut()
	assert.False(t, checkedOut)
	assert.NoError(t, err)

	assert.NoError(t, wt.Cleanup())
	assert.NoError(t, wt.Remove())
	assert.NoError(t, wt.Prune())
	assert.NoError(t, wt.RemoveWorktreeDir())
	assert.NoError(t, wt.CommitChanges("msg"))
	assert.NoError(t, wt.PushChanges("msg", true))

	// No branch / repo / base / diff.
	assert.Empty(t, wt.GetBranchName())
	assert.Empty(t, wt.GetRepoPath())
	assert.Empty(t, wt.GetRepoName())
	assert.Empty(t, wt.GetBaseCommitSHA())
	assert.False(t, wt.IsExistingBranch())
	assert.Nil(t, wt.Diff())
	assert.Nil(t, wt.DiffNumstat())

	// Cleanup is a no-op — the control dir survives (plan data outlives a
	// detach). Only Setup created it; no-op cleanups must not have removed it.
	assert.True(t, wt.WorktreeDirExists(), "no-op cleanups must not delete the control dir")
}

// TestInstance_Orchestrator_BindsHeadlessWorktree proves the factory: an
// Orchestrator instance, when its worktree is built via buildWorktree, gets a
// headlessWorktree (not a real git worktree). This is the single branch point.
func TestInstance_Orchestrator_BindsHeadlessWorktree(t *testing.T) {
	tempHome := t.TempDir()
	orig, _ := os.LookupEnv("HOME")
	require.NoError(t, os.Setenv("HOME", tempHome))
	defer func() { _ = os.Setenv("HOME", orig) }()

	repoPath := makeTempGitRepo(t)
	inst, err := NewInstance(InstanceOptions{
		Title: "orch",
		Path:  repoPath,
		ID:    "orch-factory",
		Kind:  KindOrchestrator,
	})
	require.NoError(t, err)

	wt, branch, err := inst.buildWorktree()
	require.NoError(t, err)
	assert.Empty(t, branch, "orchestrator has no branch")
	_, ok := wt.(*headlessWorktree)
	assert.True(t, ok, "orchestrator must bind a headlessWorktree")
}

// TestInstance_Worker_BindsRealWorktree proves the factory's other arm: a
// Worker instance gets a real git worktree bound to the repo. This pins that
// the refacto didn't silently change the Worker path.
func TestInstance_Worker_BindsRealWorktree(t *testing.T) {
	repoPath := makeTempGitRepo(t)
	inst, err := NewInstance(InstanceOptions{
		Title: "w",
		Path:  repoPath,
		Kind:  KindWorker,
	})
	require.NoError(t, err)

	wt, branch, err := inst.buildWorktree()
	require.NoError(t, err)
	assert.NotEmpty(t, branch, "worker gets a real branch name")
	// The real worktree is bound to the repo. Compare via resolved symlinks
	// (macOS surfaces /var as /private/var).
	repoResolved, err := filepath.EvalSymlinks(repoPath)
	require.NoError(t, err)
	wtResolved, err := filepath.EvalSymlinks(wt.GetRepoPath())
	require.NoError(t, err)
	assert.Equal(t, repoResolved, wtResolved, "worker worktree is bound to the repo")
}

// TestInstance_Kind_String pins the debug rendering.
func TestInstance_Kind_String(t *testing.T) {
	assert.Equal(t, "worker", KindWorker.String())
	assert.Equal(t, "orchestrator", KindOrchestrator.String())
}
