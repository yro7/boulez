package kernel

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yro7/boulez/session"
)

// TestKernel_Archive_KeepsRecordAndWorktree is the soft-delete contract:
// unlike Kill (which removes the record and destroys the worktree+branch),
// Archive kills only the tmux session and KEEPS the instance in the fleet
// with status Archived and a non-zero ArchivedAt, persisted so the retention
// survives a daemon restart. The instance is restorable until ReapArchived
// truly destroys it.
func TestKernel_Archive_KeepsRecordAndWorktree(t *testing.T) {
	k, spawner, state := newStorageKernel(t)

	id, err := k.Spawn(CallerContext{}, SpawnOptions{
		Repo: "/r", Title: "soft", Program: "bash",
	})
	require.NoError(t, err)
	require.Len(t, spawner.spawned, 1)

	// Archive — the syscall under test.
	require.NoError(t, k.Archive(id))

	live := k.LiveInstances()
	require.Len(t, live, 1, "archived instance is KEPT in the fleet (not removed)")
	assert.Equal(t, id, live[0].GetID())
	assert.Equal(t, "archived", live[0].Status.String(), "status is Archived")
	assert.False(t, live[0].ArchivedAt().IsZero(), "ArchivedAt is set")

	// Persisted too: a daemon restart would reload it as Archived.
	data := storedInstances(t, state)
	require.Len(t, data, 1)
	assert.Equal(t, "archived", data[0].Status.String())
	assert.False(t, data[0].ArchivedAt.IsZero(), "ArchivedAt persisted")
}

// TestKernel_Archive_UnknownID returns ErrUnknownInstance and has no side effect.
func TestKernel_Archive_UnknownID(t *testing.T) {
	k, _, _ := newStorageKernel(t)
	err := k.Archive("does-not-exist")
	assert.ErrorIs(t, err, ErrUnknownInstance{ID: "does-not-exist"})
	assert.Empty(t, k.LiveInstances())
}

// TestKernel_Archive_DistinctFromKill proves Archive and Kill diverge: a
// sibling that is Archived stays in the fleet, while a Killed one is gone.
// This guards against a future refactor collapsing the two paths.
func TestKernel_Archive_DistinctFromKill(t *testing.T) {
	k, _, _ := newStorageKernel(t)

	idA, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Title: "keep", Program: "bash"})
	require.NoError(t, err)
	idB, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Title: "gone", Program: "bash"})
	require.NoError(t, err)

	require.NoError(t, k.Archive(idA))
	require.NoError(t, k.Kill(idB))

	live := k.LiveInstances()
	require.Len(t, live, 1, "only the archived instance remains")
	assert.Equal(t, idA, live[0].GetID())
	assert.Equal(t, "archived", live[0].Status.String())
}

// TestKernel_Archive_ArchivedAtIsRecent proves the recorded timestamp is
// "now" (within tolerance) — the retention sweep keys off it.
func TestKernel_Archive_ArchivedAtIsRecent(t *testing.T) {
	k, _, _ := newStorageKernel(t)
	id, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Title: "ts", Program: "bash"})
	require.NoError(t, err)

	before := time.Now()
	require.NoError(t, k.Archive(id))
	after := time.Now()

	inst, err := k.InstanceByID(id)
	require.NoError(t, err)
	at := inst.ArchivedAt()
	assert.True(t, !at.Before(before) && !at.After(after), "ArchivedAt is ~now, got %v", at)
}

// storedInstances decodes the memStorage's persisted JSON, proving what the
// kernel wrote to disk.
func storedInstances(t *testing.T, state *memStorage) []session.InstanceData {
	t.Helper()
	var data []session.InstanceData
	require.NoError(t, json.Unmarshal(state.GetInstances(), &data))
	return data
}

// TestKernel_ReapArchived_PreservesFreshDestroysExpired proves the retention
// sweep: an Archived instance within the window is left alone; one past it is
// truly destroyed (record removed, no longer in the fleet).
func TestKernel_ReapArchived_PreservesFreshDestroysExpired(t *testing.T) {
	k, _, _ := newStorageKernel(t)
	freshID, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Title: "fresh", Program: "bash"})
	require.NoError(t, err)
	staleID, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Title: "stale", Program: "bash"})
	require.NoError(t, err)

	require.NoError(t, k.Archive(freshID))
	require.NoError(t, k.Archive(staleID))

	// Backdate the stale one past the retention window.
	require.NoError(t, k.Archive(staleID))
	backdateInstance(t, k, staleID, 25*time.Hour)
	backdateInstance(t, k, freshID, 1*time.Hour)

	k.ReapArchived(24 * time.Hour)

	live := k.LiveInstances()
	// Only the fresh archived instance remains; the stale one was reaped.
	require.Len(t, live, 1)
	assert.Equal(t, freshID, live[0].GetID(), "fresh archived instance preserved")
	assert.Equal(t, "archived", live[0].Status.String())
}

// TestKernel_ReapArchived_ZeroRetentionReapsAll proves a zero retention reaps
// every archived instance (used by tests / `boulez reset`-style flushes).
func TestKernel_ReapArchived_ZeroRetentionReapsAll(t *testing.T) {
	k, _, _ := newStorageKernel(t)
	idA, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Title: "a", Program: "bash"})
	require.NoError(t, err)
	idB, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Title: "b", Program: "bash"})
	require.NoError(t, err)
	require.NoError(t, k.Archive(idA))
	require.NoError(t, k.Archive(idB))

	k.ReapArchived(0)

	assert.Empty(t, k.LiveInstances(), "all archived instances reaped")
}

// TestKernel_ReapArchived_LeavesLiveInstancesAlone proves the sweep only
// touches Archived instances — Running/Ready/Paused are never reaped.
func TestKernel_ReapArchived_LeavesLiveInstancesAlone(t *testing.T) {
	k, _, _ := newStorageKernel(t)
	_, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Title: "live", Program: "bash"})
	require.NoError(t, err)

	k.ReapArchived(0)

	assert.Len(t, k.LiveInstances(), 1, "live instance not reaped")
}

// backdateInstance sets the instance's ArchivedAt to now - age, by reaching
// into the kernel's live instance pointer.
func backdateInstance(t *testing.T, k *Kernel, id string, age time.Duration) {
	t.Helper()
	inst, err := k.InstanceByID(id)
	require.NoError(t, err)
	inst.SetArchivedAt(time.Now().Add(-age))
}

// TestReconcileLiveness_DoesNotDemoteArchived is the regression: after Archive
// kills the tmux session, the daemon's ReconcileLiveness poll (which runs every
// second) must NOT demote the instance to Dead. An Archived instance
// intentionally has no live tmux session — that is the whole point of the soft
// delete. Before the fix, ReconcileLiveness saw the dead tmux and set the
// status to Dead, so the user's U (restore) found no Archived instances.
func TestReconcileLiveness_DoesNotDemoteArchived(t *testing.T) {
	k, _, _ := newStorageKernel(t)
	id, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Title: "soft", Program: "bash"})
	require.NoError(t, err)
	require.NoError(t, k.Archive(id))
	require.Equal(t, "archived", k.LiveInstances()[0].Status.String())

	// This is what the daemon calls every tick.
	k.ReconcileLiveness()

	inst, err := k.InstanceByID(id)
	require.NoError(t, err)
	assert.Equal(t, "archived", inst.Status.String(),
		"ReconcileLiveness must not demote an Archived instance to Dead")
}

