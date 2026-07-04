package app

import (
	"context"
	"testing"

	"claude-squad/config"
	"claude-squad/session"
	"claude-squad/ui"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFleetClient is an app-package test double for fleetClient. It serves a
// scripted snapshot of the fleet so the TUI's reconcile path (C3.2) can be
// tested without a real socket or tmux. Spawn/Pause/Resume/Kill record their
// calls so tests can assert the TUI routes mutations through the seam (C3.3).
type fakeFleetClient struct {
	list   []session.InstanceData
	listErr error

	spawned []SpawnOptions
	spawnID  string
	spawnErr error

	paused  []string
	resumed []string
	killed  []string
}

func (f *fakeFleetClient) ListInstances() ([]session.InstanceData, error) {
	return f.list, f.listErr
}

func (f *fakeFleetClient) Spawn(opts SpawnOptions) (string, error) {
	f.spawned = append(f.spawned, opts)
	if f.spawnErr != nil {
		return "", f.spawnErr
	}
	return f.spawnID, nil
}

func (f *fakeFleetClient) Pause(id string) error  { f.paused = append(f.paused, id); return nil }
func (f *fakeFleetClient) Resume(id string) error { f.resumed = append(f.resumed, id); return nil }
func (f *fakeFleetClient) Kill(id string) error   { f.killed = append(f.killed, id); return nil }

func newReconcileHome(t *testing.T, fleet fleetClient) *home {
	t.Helper()
	spin := spinner.Model{}
	list := ui.NewList(&spin, false)
	return &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
		list:      list,
		menu:      ui.NewMenu(),
		fleet:     fleet,
	}
}

func instData(id, title string, status session.Status, kind session.Kind) session.InstanceData {
	return session.InstanceData{
		ID:     id,
		Title:  title,
		Status: status,
		Kind:   kind,
		// No worktree / not started: FromInstanceData will reconstruct without
		// touching tmux (instance not started path). For reconcile tests we
		// only care about membership + lightweight state.
	}
}

// TestReconcileFleet_InitialLoad populates the view from a fresh kernel
// snapshot (C3.2): the TUI's boot read path.
func TestReconcileFleet_InitialLoad(t *testing.T) {
	fleet := &fakeFleetClient{
		list: []session.InstanceData{
			instData("w1", "worker-1", session.Running, session.KindWorker),
			instData("o1", "orch-1", session.Running, session.KindOrchestrator),
		},
	}
	h := newReconcileHome(t, fleet)

	require.NoError(t, h.refreshFleetFromKernel())

	got := h.list.GetInstances()
	require.Len(t, got, 2)
	// Orchestrator pinned to the front (view-only concern).
	assert.Equal(t, "o1", got[0].GetID(), "orchestrator pinned to front")
	assert.Equal(t, "w1", got[1].GetID())
}

// TestReconcileFleet_PreservesSelectionByID proves a refresh keeps the user's
// selection (by ID) across snapshots — the view does not jump to index 0 on
// every external mutation.
func TestReconcileFleet_PreservesSelectionByID(t *testing.T) {
	fleet := &fakeFleetClient{
		list: []session.InstanceData{
			instData("w1", "w1", session.Running, session.KindWorker),
			instData("w2", "w2", session.Running, session.KindWorker),
		},
	}
	h := newReconcileHome(t, fleet)
	require.NoError(t, h.refreshFleetFromKernel())

	// Select w2 (index 1).
	h.list.SetSelectedInstance(1)
	require.Equal(t, "w2", h.list.GetSelectedInstance().GetID())

	// A refresh that reorders nothing must keep w2 selected.
	require.NoError(t, h.refreshFleetFromKernel())
	require.Equal(t, "w2", h.list.GetSelectedInstance().GetID(),
		"selection preserved by ID across refresh")
}

// TestReconcileFleet_ReusesExistingHandles proves an unchanged instance keeps
// its *session.Instance pointer (so the background metadata tick's pointers
// stay valid and tmux bindings are not needlessly re-Restored).
func TestReconcileFleet_ReusesExistingHandles(t *testing.T) {
	fleet := &fakeFleetClient{
		list: []session.InstanceData{instData("w1", "w1", session.Running, session.KindWorker)},
	}
	h := newReconcileHome(t, fleet)
	require.NoError(t, h.refreshFleetFromKernel())

	before := h.list.GetInstances()[0]
	require.NoError(t, h.refreshFleetFromKernel())
	after := h.list.GetInstances()[0]

	assert.Same(t, before, after, "unchanged instance reuses its view handle")
}

// TestReconcileFleet_DropsRemovedInstances proves an instance absent from a
// new snapshot is removed from the view (e.g. killed via `cs2 ctl`).
func TestReconcileFleet_DropsRemovedInstances(t *testing.T) {
	fleet := &fakeFleetClient{
		list: []session.InstanceData{
			instData("w1", "w1", session.Running, session.KindWorker),
			instData("w2", "w2", session.Running, session.KindWorker),
		},
	}
	h := newReconcileHome(t, fleet)
	require.NoError(t, h.refreshFleetFromKernel())
	require.Len(t, h.list.GetInstances(), 2)

	// w1 disappears (killed externally).
	fleet.list = []session.InstanceData{instData("w2", "w2", session.Running, session.KindWorker)}
	require.NoError(t, h.refreshFleetFromKernel())

	got := h.list.GetInstances()
	require.Len(t, got, 1)
	assert.Equal(t, "w2", got[0].GetID(), "removed instance dropped from view")
}

// TestReconcileFleet_RefreshesLightweightState proves a refresh updates the
// instance's Status/AutoYes in place (the kernel is the authority) without
// replacing the handle.
func TestReconcileFleet_RefreshesLightweightState(t *testing.T) {
	fleet := &fakeFleetClient{
		list: []session.InstanceData{instData("w1", "w1", session.Running, session.KindWorker)},
	}
	h := newReconcileHome(t, fleet)
	require.NoError(t, h.refreshFleetFromKernel())

	// Kernel reports the instance is now Ready + AutoYes on.
	d := instData("w1", "w1", session.Ready, session.KindWorker)
	d.AutoYes = true
	fleet.list = []session.InstanceData{d}
	require.NoError(t, h.refreshFleetFromKernel())

	inst := h.list.GetInstances()[0]
	assert.Equal(t, session.Ready, inst.Status, "status refreshed from kernel")
	assert.True(t, inst.AutoYes, "autoyes refreshed from kernel")
}

// TestReconcileFleet_EmptySnapshotClearsView proves an empty snapshot clears
// the view (the kernel has no instances → the TUI shows none).
func TestReconcileFleet_EmptySnapshotClearsView(t *testing.T) {
	fleet := &fakeFleetClient{
		list: []session.InstanceData{instData("w1", "w1", session.Running, session.KindWorker)},
	}
	h := newReconcileHome(t, fleet)
	require.NoError(t, h.refreshFleetFromKernel())
	require.Len(t, h.list.GetInstances(), 1)

	fleet.list = nil
	require.NoError(t, h.refreshFleetFromKernel())
	assert.Empty(t, h.list.GetInstances(), "empty snapshot clears the view")
}

// TestRefreshFleetFromKernel_ListErrorIsFatal proves a socket/read error is
// surfaced (not swallowed) so boot can fail loud (decision D2).
func TestRefreshFleetFromKernel_ListErrorIsFatal(t *testing.T) {
	fleet := &fakeFleetClient{listErr: assertErr("socket unreachable")}
	h := newReconcileHome(t, fleet)
	err := h.refreshFleetFromKernel()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "socket unreachable")
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
