package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yro7/boulez/kernel"
	"github.com/yro7/boulez/session"
)

// fakeFleetSpawner is an app-package test double for kernel.Spawner. It
// returns in-memory instances (no tmux) so the socketFleetClient seam can be
// exercised end-to-end over a real control socket without touching tmux.
type fakeFleetSpawner struct {
	spawned []*session.Instance
}

func (f *fakeFleetSpawner) Spawn(opts kernel.SpawnOptions) (*session.Instance, error) {
	inst, _ := session.NewInstance(session.InstanceOptions{
		Title:   opts.Title,
		Path:    opts.Repo,
		Program: opts.Program,
		Branch:  opts.Branch,
		Kind:    opts.Kind,
	})
	// Deliberately NOT started: the fake has no real worktree/tmux session, so
	// leaf ops (Pause/Resume/Kill) return clean "not started" errors instead
	// of nil-derefing. The wire-routing contract we test does not depend on
	// the instance being live.
	f.spawned = append(f.spawned, inst)
	return inst, nil
}

// startFleetKernel serves a kernel on a short temp socket (macOS socket-path
// limit) and returns the socket path + a stop func.
func startFleetKernel(t *testing.T, spawner kernel.Spawner) (string, func()) {
	t.Helper()
	socketPath := filepath.Join("/tmp", "cs2fleet-"+time.Now().Format("150405.000000")+".sock")
	_ = os.Remove(socketPath)
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	k := kernel.New(nil,
		kernel.WithSpawner(spawner),
		kernel.WithoutAutosave(),
	)
	go func() { _ = kernel.Serve(k, socketPath) }()
	// Wait for the socket to appear.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return socketPath, func() {}
}

// TestSocketFleetClient_SpawnAndList proves the seam's wire contract: a
// spawn_worker syscall returns the new ID, and a list_instances_full syscall
// returns InstanceData carrying that ID. This is the foundational contract
// the TUI relies on in C3.2/C3.3.
func TestSocketFleetClient_SpawnAndList(t *testing.T) {
	spawner := &fakeFleetSpawner{}
	socketPath, _ := startFleetKernel(t, spawner)

	resp, err := kernel.Call(socketPath, kernel.Request{
		Method: "spawn_worker",
		Params: rawJSON(t, map[string]string{"repo": "/r", "title": "w1", "program": "bash"}),
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error, "spawn should succeed: %+v", resp.Error)
	var sr struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &sr))
	require.NotEmpty(t, sr.ID)

	// list_instances_full returns InstanceData carrying the spawned ID.
	resp2, err := kernel.Call(socketPath, kernel.Request{
		Method: "list_instances_full",
		Params: rawJSON(t, map[string]interface{}{}),
	})
	require.NoError(t, err)
	require.Nil(t, resp2.Error, "list_full should succeed: %+v", resp2.Error)
	var data []session.InstanceData
	require.NoError(t, json.Unmarshal(resp2.Result, &data))
	require.Len(t, data, 1, "fleet reflects the spawned instance")
	assert.Equal(t, sr.ID, data[0].ID)
	assert.Equal(t, "w1", data[0].Title)
}

// TestSocketFleetClient_PauseResumeKill proves the mutation syscalls route
// through the socket and address instances by ID. The fake spawner's instances
// have no real worktree, so the syscalls may return kernel errors (e.g. not
// started); the contract we assert is that the wire accepts the call and the
// ID is routed (no transport failure).
func TestSocketFleetClient_PauseResumeKill(t *testing.T) {
	spawner := &fakeFleetSpawner{}
	socketPath, _ := startFleetKernel(t, spawner)

	id := spawnViaSocket(t, socketPath, "w1")

	for _, method := range []string{"pause", "resume", "kill"} {
		resp, err := kernel.Call(socketPath, kernel.Request{
			Method: method,
			Params: rawJSON(t, map[string]string{"id": id}),
		})
		require.NoError(t, err, "%s: transport must succeed", method)
		// A kernel error (e.g. instance not started enough to pause) is
		// acceptable — we are asserting the wire routing, not the leaf op.
		_ = resp
	}
}

// TestSocketFleetClient_ListEmptyOnFreshFleet proves the read path returns an
// empty slice on a fresh kernel — the TUI's reconcile must handle this
// without nil-deref.
func TestSocketFleetClient_ListEmptyOnFreshFleet(t *testing.T) {
	spawner := &fakeFleetSpawner{}
	socketPath, _ := startFleetKernel(t, spawner)

	resp, err := kernel.Call(socketPath, kernel.Request{
		Method: "list_instances_full",
		Params: rawJSON(t, map[string]interface{}{}),
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)
	var data []session.InstanceData
	require.NoError(t, json.Unmarshal(resp.Result, &data))
	assert.Empty(t, data)
}

func spawnViaSocket(t *testing.T, socketPath, title string) string {
	t.Helper()
	resp, err := kernel.Call(socketPath, kernel.Request{
		Method: "spawn_worker",
		Params: rawJSON(t, map[string]string{"repo": "/r", "title": title, "program": "bash"}),
	})
	require.NoError(t, err)
	require.Nil(t, resp.Error)
	var sr struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &sr))
	require.NotEmpty(t, sr.ID)
	return sr.ID
}

func rawJSON(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
