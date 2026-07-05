package kernel

import (
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/yro7/boulez/cmd"
	"github.com/yro7/boulez/cmd/cmd_test"
	"github.com/yro7/boulez/config"
	"github.com/yro7/boulez/host"
	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/session/tmux"

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

func (m *memStorage) GetHelpScreensSeen() uint32 { return m.helpSeen }
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

// TestKernel_Kill_RemovesRecordEvenWhenCleanupFails is the regression for the
// unkillable-zombie: when inst.Kill() partially fails (e.g. a Dead instance
// whose tmux session is already gone, or a worktree whose branch is checked
// out elsewhere), the kernel MUST still remove the record from the fleet
// and from storage. Before the fix, Kill returned the cleanup error early
// without removing the record, so a Dead instance accumulated forever — only
// `boulez reset` could clear it.
func TestKernel_Kill_RemovesRecordEvenWhenCleanupFails(t *testing.T) {
	k, spawner, state := newStorageKernel(t)

	// Build a TmuxSession whose kill-session fails but whose has-session
	// reports the session as STILL EXISTING — so Close() is NOT idempotent
	// and surfaces a real error. This models a dead instance whose tmux
	// session is wedged (present but unkillable), the worst case for the
	// zombie regression.
	mockExec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			if strings.Contains(cmd.ToString(c), "has-session") {
				return nil // session "exists" → Close won't swallow the kill error
			}
			if strings.Contains(cmd.ToString(c), "kill-session") {
				return &exec.ExitError{} // kill fails
			}
			return nil
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return []byte{}, nil },
	}
	spawner.tmuxInjector = func(inst *session.Instance) {
		inst.SetTmuxSession(tmux.NewTmuxSessionWithDeps("wedge", "bash",
			host.LocalPtyFactory(), mockExec))
	}

	id, err := k.Spawn(CallerContext{}, SpawnOptions{
		Repo: "/r", Title: "wedge", Program: "bash",
	})
	require.NoError(t, err)
	require.Len(t, k.LiveInstances(), 1)

	// Kill — inst.Kill() errors (tmux kill-session fails on a "live" session).
	err = k.Kill(id)
	require.Error(t, err, "cleanup error is surfaced to the caller")
	assert.Contains(t, err.Error(), "kill",
		"the surfaced error is the tmux kill failure")

	// But the record is GONE from the fleet — no zombie.
	assert.Empty(t, k.LiveInstances(),
		"record removed even when cleanup fails")
	assert.NotContains(t, storedTitles(t, state), "wedge",
		"record not re-persisted")
	assert.Empty(t, storedTitles(t, state))
}

// Compile-time: memStorage satisfies config.InstanceStorage (and AppState).
var (
	_ config.InstanceStorage = (*memStorage)(nil)
	_ config.AppState        = (*memStorage)(nil)
)
