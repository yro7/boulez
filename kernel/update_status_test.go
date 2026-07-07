package kernel

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yro7/boulez/session"
)

// storedStatusByTitle decodes the memStorage's persisted JSON and returns a
// title→status map, proving exactly what the kernel wrote to disk.
func storedStatusByTitle(t *testing.T, state *memStorage) map[string]session.Status {
	t.Helper()
	var data []session.InstanceData
	require.NoError(t, json.Unmarshal(state.GetInstances(), &data))
	out := make(map[string]session.Status, len(data))
	for _, d := range data {
		out[d.Title] = d.Status
	}
	return out
}

// TestKernel_UpdateStatus_PersistsOnChange proves UpdateStatus mutates the
// in-memory instance AND persists the transition to storage. This is the
// status-authority contract: a stabilized Ready must survive a daemon restart
// / a reconnecting TUI / `boulez ctl list_instances`, not stay frozen at the
// spawn-time Running.
func TestKernel_UpdateStatus_PersistsOnChange(t *testing.T) {
	k, _, state := newStorageKernel(t)

	id, err := k.Spawn(CallerContext{}, SpawnOptions{
		Repo: "/r", Title: "w1", Program: "pi",
	})
	require.NoError(t, err)

	// Spawn persists Running.
	require.Equal(t, session.Running, storedStatusByTitle(t, state)["w1"])

	k.UpdateStatus(id, session.Ready)

	// In-memory and persisted both reflect the transition.
	inst, err := k.GetInstance(id)
	require.NoError(t, err)
	assert.Equal(t, session.Ready, inst.Status, "in-memory status updated")
	assert.Equal(t, session.Ready, storedStatusByTitle(t, state)["w1"],
		"persisted status updated (survives restart / visible to ctl list_instances)")
}

// TestKernel_UpdateStatus_NoOpWhenUnchanged proves an unchanged status does
// not trigger a disk write. Persisting only on a real transition avoids a
// write per poll tick for an idle instance.
func TestKernel_UpdateStatus_NoOpWhenUnchanged(t *testing.T) {
	k, _, state := newStorageKernel(t)

	id, err := k.Spawn(CallerContext{}, SpawnOptions{
		Repo: "/r", Title: "w1", Program: "pi",
	})
	require.NoError(t, err)

	afterSpawn := state.GetInstances()
	k.UpdateStatus(id, session.Running) // same as spawn status
	assert.Equal(t, afterSpawn, state.GetInstances(),
		"no persistence when status is unchanged")
}

// TestKernel_UpdateStatus_NoOpWhenUnknownID proves a bogus id is a silent
// no-op, not a panic. The poll loop calls UpdateStatus for every live
// instance; a race where an instance is killed mid-poll must not crash the
// daemon.
func TestKernel_UpdateStatus_NoOpWhenUnknownID(t *testing.T) {
	k := New(nil, WithoutAutosave()) // pure in-memory, no storage

	assert.NotPanics(t, func() { k.UpdateStatus("does-not-exist", session.Ready) })
}
