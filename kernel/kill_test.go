package kernel

import (
	"encoding/json"
	"sync"
	"testing"

	"claude-squad/config"
	"claude-squad/session"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memStorage is an in-memory config.InstanceStorage for kernel tests that
// need to assert what the kernel persisted (C4.1: Kill must remove the
// instance from the fleet AND from storage, not re-save it as a zombie).
type memStorage struct {
	mu       sync.Mutex
	raw      json.RawMessage
	helpSeen uint32
}

func newMemStorage() *memStorage {
	return &memStorage{raw: json.RawMessage("[]")}
}

func (m *memStorage) SaveInstances(instancesJSON json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.raw = append(json.RawMessage(nil), instancesJSON...)
	return nil
}

func (m *memStorage) GetInstances() json.RawMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append(json.RawMessage(nil), m.raw...)
}

func (m *memStorage) DeleteAllInstances() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.raw = json.RawMessage("[]")
	return nil
}

func (m *memStorage) GetHelpScreensSeen() uint32      { return m.helpSeen }
func (m *memStorage) SetHelpScreensSeen(seen uint32) error {
	m.helpSeen = seen
	return nil
}

// newStorageKernel builds a kernel whose persistence is observable through the
// returned memStorage. autosave is ON (the production path the zombie bug
// hits).
func newStorageKernel(t *testing.T) (*Kernel, *fakeSpawner, *memStorage) {
	t.Helper()
	spawner := &fakeSpawner{}
	state := newMemStorage()
	storage := NewStorage(state)
	k := New(storage,
		WithSpawner(spawner),
		WithMerger(&fakeMerger{}),
		// autosave ON (default) — the bug only manifests when persist runs.
	)
	return k, spawner, state
}

// storedTitles decodes the memStorage's persisted JSON into titles, proving
// exactly what the kernel wrote to disk (the zombie would show up here).
func storedTitles(t *testing.T, state *memStorage) []string {
	t.Helper()
	var data []session.InstanceData
	require.NoError(t, json.Unmarshal(state.GetInstances(), &data))
	out := make([]string, 0, len(data))
	for _, d := range data {
		out = append(out, d.Title)
	}
	return out
}

// TestKernel_Kill_RemovesFromFleetAndStorage is the C4.1 regression: Kill
// must drop the instance from LiveInstances (no in-memory zombie) AND from
// persisted storage (no on-disk zombie that reappears after a daemon
// restart). Before the fix, Kill called persist WITHOUT removing the instance
// from the store, so the just-killed instance was re-saved and resurrected
// on the next boot.
func TestKernel_Kill_RemovesFromFleetAndStorage(t *testing.T) {
	k, spawner, state := newStorageKernel(t)

	id, err := k.Spawn(CallerContext{}, SpawnOptions{
		Repo: "/r", Title: "zombie-bait", Program: "bash",
	})
	require.NoError(t, err)
	require.Len(t, spawner.spawned, 1)

	// Sanity: the spawned instance is live and persisted.
	require.Len(t, k.LiveInstances(), 1)
	assert.Contains(t, storedTitles(t, state), "zombie-bait")

	// Kill — the syscall under test.
	require.NoError(t, k.Kill(id))

	// No in-memory zombie.
	assert.Empty(t, k.LiveInstances(), "killed instance must not linger in the fleet")

	// No on-disk zombie either: persist must have written the fleet WITHOUT
	// the killed instance, so a daemon restart cannot resurrect it.
	assert.NotContains(t, storedTitles(t, state), "zombie-bait",
		"killed instance must not be re-persisted")
	assert.Empty(t, storedTitles(t, state))
}

// TestKernel_Kill_UnknownID returns ErrUnknownInstance and has no side effect.
func TestKernel_Kill_UnknownID(t *testing.T) {
	k, _, _ := newStorageKernel(t)
	err := k.Kill("does-not-exist")
	assert.ErrorIs(t, err, ErrUnknownInstance{ID: "does-not-exist"})
	assert.Empty(t, k.LiveInstances())
}

// TestKernel_Kill_LeavesSiblingInstancesAlone ensures remove() targets only
// the killed ID, not the whole store.
func TestKernel_Kill_LeavesSiblingInstancesAlone(t *testing.T) {
	k, _, state := newStorageKernel(t)

	idA, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Title: "a", Program: "bash"})
	require.NoError(t, err)
	idB, err := k.Spawn(CallerContext{}, SpawnOptions{Repo: "/r", Title: "b", Program: "bash"})
	require.NoError(t, err)

	require.NoError(t, k.Kill(idA))

	live := k.LiveInstances()
	require.Len(t, live, 1)
	assert.Equal(t, idB, live[0].GetID())
	assert.ElementsMatch(t, []string{"b"}, storedTitles(t, state))
}

// Compile-time: memStorage satisfies config.InstanceStorage (and AppState).
var (
	_ config.InstanceStorage = (*memStorage)(nil)
	_ config.AppState        = (*memStorage)(nil)
)
