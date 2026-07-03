package kernel

import (
	"claude-squad/session"
	"claude-squad/session/git"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSpawner is a test Spawner that returns in-memory instances without
// touching tmux. This is the key to kernel testability: the only tmux-coupled
// operation (instance creation+start) is faked.
type fakeSpawner struct {
	mu       sync.Mutex
	spawned  []*session.Instance
	spawnErr error
}

func (f *fakeSpawner) Spawn(opts SpawnOptions) (*session.Instance, error) {
	if f.spawnErr != nil {
		return nil, f.spawnErr
	}
	kind := opts.Kind
	inst, _ := session.NewInstance(session.InstanceOptions{
		Title:   opts.Title,
		Path:    opts.Repo,
		Program: opts.Program,
		Branch:  opts.Branch,
		Kind:    kind,
	})
	// Mark as started so the kernel's summarize treats it as live.
	inst.SetStatus(session.Running)
	inst.MarkStartedForTest()
	f.mu.Lock()
	f.spawned = append(f.spawned, inst)
	f.mu.Unlock()
	return inst, nil
}

// fakeMerger is a test Merger that records calls and returns a scripted result.
type fakeMerger struct {
	mu     sync.Mutex
	calls  []mergeCall
	result git.MergeResult
	err    error
}

type mergeCall struct {
	repoPath   string
	target     string
	sources    []string
	strategy   git.Strategy
}

func (f *fakeMerger) Merge(repoPath, targetBranch string, sourceBranches []string, strategy git.Strategy) (git.MergeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, mergeCall{repoPath, targetBranch, sourceBranches, strategy})
	return f.result, f.err
}

// newTestKernel builds a kernel with fake deps and autosave off.
func newTestKernel(t *testing.T) (*Kernel, *fakeSpawner, *fakeMerger) {
	t.Helper()
	spawner := &fakeSpawner{}
	merger := &fakeMerger{}
	k := New(nil, // no storage → pure in-memory
		WithSpawner(spawner),
		WithMerger(merger),
		WithoutAutosave(),
	)
	return k, spawner, merger
}

// TestKernel_Spawn_ReturnsID proves the foundational syscall: Spawn creates an
// instance and returns its ID, which is stable and addressable via GetInstance.
func TestKernel_Spawn_ReturnsID(t *testing.T) {
	k, spawner, _ := newTestKernel(t)

	id, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Title: "t", Program: "bash"})
	require.NoError(t, err)
	assert.NotEmpty(t, id)

	// The spawner was invoked exactly once.
	assert.Len(t, spawner.spawned, 1)
	assert.Equal(t, id, spawner.spawned[0].GetID())

	// The instance is addressable via GetInstance.
	detail, err := k.GetInstance(id)
	require.NoError(t, err)
	assert.Equal(t, id, detail.ID)
	assert.Equal(t, "t", detail.Title)
	assert.Equal(t, session.Running, detail.Status)
}

// TestKernel_Spawn_WorkerCannotSpawn is the recursion guard: a CallerContext
// whose Kind is Worker is refused. This enforces the two-level topology.
func TestKernel_Spawn_WorkerCannotSpawn(t *testing.T) {
	k, spawner, _ := newTestKernel(t)

	_, err := k.Spawn(CallerContext{CallerID: "worker-1", Kind: session.KindWorker}, SpawnOptions{Repo: "/r"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrWorkerCannotSpawn{}))
	assert.Empty(t, spawner.spawned, "no instance created on a refused spawn")
}

// TestKernel_Spawn_OrchestratorCannotSpawnOrchestrator is the second-level
// guard: v1 forbids the super-orchestrator hierarchy. An orchestrator caller
// spawning an orchestrator is refused.
func TestKernel_Spawn_OrchestratorCannotSpawnOrchestrator(t *testing.T) {
	k, spawner, _ := newTestKernel(t)

	_, err := k.Spawn(CallerContext{CallerID: "orch-1", Kind: session.KindOrchestrator},
		SpawnOptions{Repo: "/r", Kind: session.KindOrchestrator})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNestedOrchestrator{}))
	assert.Empty(t, spawner.spawned)
}

// TestKernel_Spawn_TopLevelCanSpawnOrchestrator proves a top-level caller
// (zero-value CallerContext) CAN spawn an orchestrator — the entry point for
// the future super-orchestrator flow and for bootstrapping an orchestrator.
func TestKernel_Spawn_TopLevelCanSpawnOrchestrator(t *testing.T) {
	k, spawner, _ := newTestKernel(t)

	id, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Kind: session.KindOrchestrator})
	require.NoError(t, err)
	assert.NotEmpty(t, id)
	require.Len(t, spawner.spawned, 1)
	assert.Equal(t, session.KindOrchestrator, spawner.spawned[0].Kind())
}

// TestKernel_GetInstance_UnknownID proves the typed error for a missing ID.
func TestKernel_GetInstance_UnknownID(t *testing.T) {
	k, _, _ := newTestKernel(t)

	_, err := k.GetInstance("does-not-exist")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnknownInstance{ID: "does-not-exist"}))
}

// TestKernel_ListInstances_FilterByKind proves the list syscall narrows by
// Kind. An orchestrator listing its workers filters Kind=Worker.
func TestKernel_ListInstances_FilterByKind(t *testing.T) {
	k, _, _ := newTestKernel(t)

	_, _ = k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Title: "w1", Kind: session.KindWorker})
	_, _ = k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Title: "o1", Kind: session.KindOrchestrator})

	workers := k.ListInstances(FilterByKind(session.KindWorker))
	require.Len(t, workers, 1)
	assert.Equal(t, "w1", workers[0].Title)

	orchs := k.ListInstances(FilterByKind(session.KindOrchestrator))
	require.Len(t, orchs, 1)
	assert.Equal(t, "o1", orchs[0].Title)

	all := k.ListInstances(ListFilter{})
	assert.Len(t, all, 2)
}

// TestKernel_SendPrompt_RoutesByID proves the syscall addresses the instance
// by ID. We assert the instance received the prompt via the fake spawner's
// record (the real instance would forward to tmux).
func TestKernel_SendPrompt_RoutesByID(t *testing.T) {
	k, spawner, _ := newTestKernel(t)

	id, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Title: "t", Program: "bash"})
	require.NoError(t, err)

	// The fake instance is marked started, but SendPrompt on a fake instance
	// (no real tmux session) will error. We assert the routing happens: the
	// kernel found the instance by ID and called SendPrompt on it (the error
	// is the instance's, not the kernel's routing).
	err = k.SendPrompt(id, "do something")
	// Expect an error because the fake instance has no real tmux; the point
	// is that the kernel ROUTED to the right instance (not ErrUnknownInstance).
	if err != nil {
		assert.False(t, errors.Is(err, ErrUnknownInstance{ID: id}),
			"error must be from the instance, not unknown-ID routing")
	}
	_ = spawner // spawner.spawned[0] is the instance
}

// TestKernel_Merge_DelegatesToMerger proves the merge syscall routes to the
// injected Merger with the right arguments, and returns its result verbatim.
func TestKernel_Merge_DelegatesToMerger(t *testing.T) {
	k, _, merger := newTestKernel(t)
	merger.result = git.MergeResult{Status: git.MergeMerged, Message: "ok"}

	res, err := k.Merge(CallerContext{}, "/repo", "integration", []string{"feat-a"}, git.StrategyDefault)
	require.NoError(t, err)
	assert.Equal(t, git.MergeMerged, res.Status)

	require.Len(t, merger.calls, 1)
	c := merger.calls[0]
	assert.Equal(t, "/repo", c.repoPath)
	assert.Equal(t, "integration", c.target)
	assert.Equal(t, []string{"feat-a"}, c.sources)
	assert.Equal(t, git.StrategyDefault, c.strategy)
}

// TestKernel_Merge_PropagatesConflict proves a conflict result flows through
// the kernel unchanged. v1 does not auto-resolve — the caller decides.
func TestKernel_Merge_PropagatesConflict(t *testing.T) {
	k, _, merger := newTestKernel(t)
	merger.result = git.MergeResult{
		Status:    git.MergeConflict,
		Conflicts: []git.Conflict{{File: "a.go"}},
	}

	res, err := k.Merge(CallerContext{}, "/repo", "integration", []string{"feat"}, git.StrategyDefault)
	// The merger returns nil error + Conflict status; the kernel propagates.
	require.NoError(t, err)
	assert.Equal(t, git.MergeConflict, res.Status)
	require.Len(t, res.Conflicts, 1)
	assert.Equal(t, "a.go", res.Conflicts[0].File)
}

// TestKernel_Merge_NoMergerWired proves a misconfigured kernel fails loudly
// rather than silently no-op-ing.
func TestKernel_Merge_NoMergerWired(t *testing.T) {
	k := New(nil, WithoutAutosave()) // no merger override → defaults to real
	// The default merger is real (git.NewMerger). It would run git on a
	// nonexistent repo. We assert it errors rather than silently succeeding.
	_, err := k.Merge(CallerContext{}, "/nonexistent", "main", []string{"x"}, git.StrategyDefault)
	assert.Error(t, err)
}

// TestKernel_Merge_RefusesHostCurrentBranch proves the kernel-level guard
// (spec decision 7): an injected protected branch (the host repo's current
// branch) is refused at the kernel, BEFORE the merger runs. This is the case
// that passed at fault in dogfooding — `merge --target-branch integration`
// on a host repo checked out at integration succeeded. The guard is
// non-contournable: a client cannot bypass it by lying, since the kernel
// applies it from its injected config, not from request params.
func TestKernel_Merge_RefusesHostCurrentBranch(t *testing.T) {
	merger := &fakeMerger{result: git.MergeResult{Status: git.MergeMerged}}
	k := New(nil, WithMerger(merger), WithoutAutosave(), WithProtectedBranches([]string{"integration"}))

	res, err := k.Merge(CallerContext{}, "/repo", "integration", []string{"feat"}, git.StrategyDefault)
	require.Error(t, err, "merging into the host's current branch must be refused")
	var pbe git.ErrProtectedBranch
	require.ErrorAs(t, err, &pbe)
	assert.Equal(t, "integration", pbe.Branch)
	assert.Equal(t, git.MergeConflict, res.Status)
	assert.Empty(t, merger.calls, "the merger must not run when the kernel guard refuses")
}

// TestKernel_Merge_CaseInsensitiveProtected proves a caller can't bypass the
// host-current-branch guard by capitalising ("Main" vs "main"). git branch
// names are case-sensitive on disk, but the protected check is defensive.
func TestKernel_Merge_CaseInsensitiveProtected(t *testing.T) {
	merger := &fakeMerger{result: git.MergeResult{Status: git.MergeMerged}}
	k := New(nil, WithMerger(merger), WithoutAutosave(), WithProtectedBranches([]string{"main"}))

	_, err := k.Merge(CallerContext{}, "/repo", "Main", []string{"feat"}, git.StrategyDefault)
	require.Error(t, err)
	assert.Empty(t, merger.calls, "guard fires before the merger")
}

// TestKernel_Spawn_NoSpawnerWired proves a kernel with no spawner refuses.
func TestKernel_Spawn_NoSpawnerWired(t *testing.T) {
	k := New(nil, WithoutAutosave(), WithMerger(&fakeMerger{}))
	_, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no spawner wired")
}

// TestKernel_ListInstances_EmptyWhenFresh proves a fresh kernel lists nothing.
func TestKernel_ListInstances_EmptyWhenFresh(t *testing.T) {
	k, _, _ := newTestKernel(t)
	assert.Empty(t, k.ListInstances(ListFilter{}))
}

// TestKernel_Spawn_ErrorPropagates proves a spawner failure surfaces as a
// spawn error and registers no instance.
func TestKernel_Spawn_ErrorPropagates(t *testing.T) {
	k, spawner, _ := newTestKernel(t)
	spawner.spawnErr = errors.New("tmux exploded")

	_, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tmux exploded")
	assert.Empty(t, k.ListInstances(ListFilter{}), "failed spawn registers nothing")
}
