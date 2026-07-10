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
