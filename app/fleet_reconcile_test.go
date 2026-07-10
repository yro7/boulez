package app

import (
	"context"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yro7/boulez/config"
	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/ui"
)

// fakeFleetClient is an app-package test double for fleetClient. It serves a
// scripted snapshot of the fleet so the TUI's reconcile path (C3.2) can be
// tested without a real socket or tmux. Spawn/Pause/Resume/Kill record their
// calls so tests can assert the TUI routes mutations through the seam (C3.3).
type fakeFleetClient struct {
	list    []session.InstanceData
	listErr error

	spawned  []SpawnOptions
	spawnID  string
	spawnErr error

	paused   []string
	resumed  []string
	killed   []string
	archived []string
	restored []string
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

func (f *fakeFleetClient) Pause(id string) error    { f.paused = append(f.paused, id); return nil }
func (f *fakeFleetClient) Resume(id string) error   { f.resumed = append(f.resumed, id); return nil }
func (f *fakeFleetClient) Kill(id string) error     { f.killed = append(f.killed, id); return nil }
func (f *fakeFleetClient) Archive(id string) error  { f.archived = append(f.archived, id); return nil }
func (f *fakeFleetClient) Restore(id string) error { f.restored = append(f.restored, id); return nil }

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
// new snapshot is removed from the view (e.g. killed via `boulez ctl`).
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

// TestReconcileFleet_PreservesLandedHint proves the TUI-only landed/landing
// hints survive a fleet refresh: reconcileFleet reuses existing handles by ID
// and resets only the kernel-owned lightweight state (Status, AutoYes), so the
// view-only landed hint (set after a successful land, cleared on Running→Ready)
// is NOT wiped on the periodic fleet tick. Without this, a land's visual
// indicator would flicker off on the next 1s tick.
func TestReconcileFleet_PreservesLandedHint(t *testing.T) {
	fleet := &fakeFleetClient{
		list: []session.InstanceData{instData("w1", "w1", session.Ready, session.KindWorker)},
	}
	h := newReconcileHome(t, fleet)
	require.NoError(t, h.refreshFleetFromKernel())

	inst := h.list.GetInstances()[0]
	inst.SetLanded(true)
	inst.SetLanding(true)

	// A fleet tick arrives (same instance, status unchanged).
	require.NoError(t, h.refreshFleetFromKernel())

	after := h.list.GetInstances()[0]
	assert.True(t, after.Landed(), "landed hint survives fleet refresh")
	assert.True(t, after.Landing(), "landing hint survives fleet refresh")
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

// TestReconcileFleet_PreservesDraftDuringNameEntry is the regression test for
// the index-out-of-range panic: the fleet tick fires every second while the
// user is typing a name for a new instance. The draft instance is held in the
// list but is NOT yet known to the kernel (no spawn has been issued), so the
// kernel snapshot does not contain it. Before the fix, reconcileFleet rebuilt
// the view from the snapshot alone, dropping the draft; the next keystroke in
// stateNew then panicked on m.list.GetInstances()[m.list.NumInstances()-1]
// (index -1). The draft must be preserved against the snapshot.
func TestReconcileFleet_PreservesDraftDuringNameEntry(t *testing.T) {
	fleet := &fakeFleetClient{
		list: []session.InstanceData{instData("w1", "w1", session.Running, session.KindWorker)},
	}
	h := newReconcileHome(t, fleet)
	require.NoError(t, h.refreshFleetFromKernel())

	// Simulate startNewInstance adding a local draft (name entry). The draft
	// has a TUI-local ID the kernel does not know about.
	draft, err := session.NewInstance(session.InstanceOptions{
		Title: "", Path: "/repo", Program: "claude",
	})
	require.NoError(t, err)
	h.list.AddInstance(draft)
	h.trackDraft(draft.GetID())
	h.list.SetSelectedInstance(h.list.NumInstances() - 1)
	require.Len(t, h.list.GetInstances(), 2, "draft added to the view")

	// A fleet tick fires: the kernel snapshot still has only w1 (the draft is
	// not spawned yet). The draft MUST survive this refresh.
	require.NoError(t, h.refreshFleetFromKernel())

	got := h.list.GetInstances()
	require.Len(t, got, 2, "draft preserved against fleet snapshot")
	ids := map[string]bool{got[0].GetID(): true, got[1].GetID(): true}
	assert.True(t, ids[draft.GetID()], "draft ID still present in the view")
	assert.True(t, ids["w1"], "kernel instance still present")

	// And the stateNew handler's index access no longer panics on an empty list.
	selected := h.list.GetSelectedInstance()
	require.NotNil(t, selected, "selection preserved (no -1 index panic)")
}

// TestReconcileFleet_UntrackedLocalInstanceIsDropped proves the fix is
// scoped: only TRACKED drafts (pending spawn / name entry) are preserved. A
// stale local instance that is not in the kernel snapshot and not tracked is
// dropped (e.g. a killed instance whose removal raced the tick).
func TestReconcileFleet_UntrackedLocalInstanceIsDropped(t *testing.T) {
	fleet := &fakeFleetClient{
		list: []session.InstanceData{instData("w1", "w1", session.Running, session.KindWorker)},
	}
	h := newReconcileHome(t, fleet)
	require.NoError(t, h.refreshFleetFromKernel())

	// Add a local instance that is NOT tracked as a draft (simulating a stale
	// leftover from a killed-kernel race). It is not in the kernel snapshot.
	stale, err := session.NewInstance(session.InstanceOptions{
		Title: "stale", Path: "/repo", Program: "claude",
	})
	require.NoError(t, err)
	h.list.AddInstance(stale)
	require.Len(t, h.list.GetInstances(), 2)

	require.NoError(t, h.refreshFleetFromKernel())
	got := h.list.GetInstances()
	require.Len(t, got, 1, "untracked local instance dropped")
	assert.Equal(t, "w1", got[0].GetID())
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

// TestReconcileFleet_HidesArchivedInstances proves a soft-deleted (Archived)
// instance is filtered out of the normal fleet view — the user hit Ctrl+D,
// so it should disappear from the list. The instance remains in the kernel
// (restorable until ReapArchived) but is not shown.
func TestReconcileFleet_HidesArchivedInstances(t *testing.T) {
	fleet := &fakeFleetClient{
		list: []session.InstanceData{
			instData("w1", "w1", session.Running, session.KindWorker),
			instData("w2", "w2", session.Archived, session.KindWorker),
			instData("w3", "w3", session.Ready, session.KindWorker),
		},
	}
	h := newReconcileHome(t, fleet)
	require.NoError(t, h.refreshFleetFromKernel())

	got := h.list.GetInstances()
	require.Len(t, got, 2, "archived instance hidden from the view")
	for _, inst := range got {
		assert.NotEqual(t, session.Archived, inst.Status,
			"no archived instance in the list")
	}
}
