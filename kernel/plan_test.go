package kernel

import (
	"os"
	"testing"

	"claude-squad/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withTempHome isolates HOME so plan files land under a temp dir.
func withTempHome(t *testing.T) func() {
	t.Helper()
	dir := t.TempDir()
	orig, had := os.LookupEnv("HOME")
	require.NoError(t, os.Setenv("HOME", dir))
	return func() {
		if had {
			_ = os.Setenv("HOME", orig)
		} else {
			_ = os.Unsetenv("HOME")
		}
	}
}

// TestPlan_SaveLoad_RoundTrip proves a plan survives save→load: the
// resumability contract. An orchestrator's plan (workers + targets + state)
// must persist across a cs2 restart.
func TestPlan_SaveLoad_RoundTrip(t *testing.T) {
	restore := withTempHome(t)
	defer restore()

	p := &OrchestratorPlan{
		ID:        "orch-1",
		WorkerIDs: []string{"w-1", "w-2"},
		MergeTargets: []MergeTarget{{Repo: "/r", Branch: "integration", Sources: []string{"feat-a"}}},
		State:     PlanRunning,
	}
	require.NoError(t, SavePlan(p))

	loaded, err := LoadPlan("orch-1")
	require.NoError(t, err)
	assert.Equal(t, p.ID, loaded.ID)
	assert.Equal(t, p.WorkerIDs, loaded.WorkerIDs)
	assert.Equal(t, p.MergeTargets, loaded.MergeTargets)
	assert.Equal(t, PlanRunning, loaded.State)
}

// TestPlan_LoadMissing proves LoadPlan returns an os.IsNotExist-compatible
// error for a fresh orchestrator that has never been persisted.
func TestPlan_LoadMissing(t *testing.T) {
	restore := withTempHome(t)
	defer restore()

	_, err := LoadPlan("never-existed")
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err), "missing plan must satisfy os.IsNotExist")
}

// TestPlan_RecordWorker_Appends proves recordWorkerInPlan adds a worker and
// creates the plan in Running state on first use.
func TestPlan_RecordWorker_Appends(t *testing.T) {
	restore := withTempHome(t)
	defer restore()

	require.NoError(t, recordWorkerInPlan("orch-2", "w-a"))
	p, err := LoadPlan("orch-2")
	require.NoError(t, err)
	assert.Equal(t, PlanRunning, p.State)
	assert.Equal(t, []string{"w-a"}, p.WorkerIDs)

	// A second worker appends without duplicating.
	require.NoError(t, recordWorkerInPlan("orch-2", "w-b"))
	require.NoError(t, recordWorkerInPlan("orch-2", "w-a")) // dedup
	p, err = LoadPlan("orch-2")
	require.NoError(t, err)
	assert.Equal(t, []string{"w-a", "w-b"}, p.WorkerIDs)
}

// TestPlan_ListPlans proves ListPlans returns all persisted plans, sorted.
func TestPlan_ListPlans(t *testing.T) {
	restore := withTempHome(t)
	defer restore()

	require.NoError(t, SavePlan(&OrchestratorPlan{ID: "b", State: PlanRunning}))
	require.NoError(t, SavePlan(&OrchestratorPlan{ID: "a", State: PlanDone}))

	plans, err := ListPlans()
	require.NoError(t, err)
	require.Len(t, plans, 2)
	assert.Equal(t, "a", plans[0].ID, "sorted by ID")
	assert.Equal(t, "b", plans[1].ID)
}

// TestPlan_ListPlans_EmptyWhenNone proves ListPlans returns nil/nil for a
// fresh config dir with no orchestrators.
func TestPlan_ListPlans_EmptyWhenNone(t *testing.T) {
	restore := withTempHome(t)
	defer restore()

	plans, err := ListPlans()
	require.NoError(t, err)
	assert.Empty(t, plans)
}

// TestPlan_Save_RequiresID proves the guard.
func TestPlan_Save_RequiresID(t *testing.T) {
	restore := withTempHome(t)
	defer restore()
	err := SavePlan(&OrchestratorPlan{ID: ""})
	require.Error(t, err)
}

// TestKernel_Spawn_RecordsWorkerInPlan proves the kernel wires the plan hook:
// when an orchestrator spawns a worker, the worker ID lands in the
// orchestrator's plan. This is the spawn half of resumability.
func TestKernel_Spawn_RecordsWorkerInPlan(t *testing.T) {
	restore := withTempHome(t)
	defer restore()

	spawner := &fakeSpawner{}
	k := New(nil, WithSpawner(spawner), WithMerger(&fakeMerger{}), WithoutAutosave())

	// Bootstrap an orchestrator, then have it spawn a worker.
	orchID, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Kind: session.KindOrchestrator})
	require.NoError(t, err)

	workerID, err := k.Spawn(CallerContext{CallerID: orchID, Kind: session.KindOrchestrator},
		SpawnOptions{Repo: "/r", Kind: session.KindWorker})
	require.NoError(t, err)

	plan, err := LoadPlan(orchID)
	require.NoError(t, err)
	assert.Equal(t, PlanRunning, plan.State)
	assert.Contains(t, plan.WorkerIDs, workerID, "spawned worker recorded in the orchestrator's plan")
}

// TestKernel_Spawn_TopLevelSpawnDoesNotTouchPlan proves a top-level spawn
// (cs2 ctl) does NOT create a plan — plans are orchestrator-scoped.
func TestKernel_Spawn_TopLevelSpawnDoesNotTouchPlan(t *testing.T) {
	restore := withTempHome(t)
	defer restore()

	spawner := &fakeSpawner{}
	k := New(nil, WithSpawner(spawner), WithMerger(&fakeMerger{}), WithoutAutosave())

	id, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r"})
	require.NoError(t, err)

	_, err = LoadPlan(id)
	require.Error(t, err, "top-level spawn creates no plan")
	assert.True(t, os.IsNotExist(err))
}
