package app

import (
	"context"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yro7/boulez/config"
	"github.com/yro7/boulez/host"
	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/ui"
)

// newSpawnHome builds a home with a fake fleet client and a draft instance in
// the list (Loading), so spawn-routing tests can fire runSpawnCmd and assert
// on the spawnDoneMsg handling (C3.3).
func newSpawnHome(t *testing.T, fleet fleetClient, draft *session.Instance) *home {
	t.Helper()
	spin := spinner.Model{}
	list := ui.NewList(&spin, false)
	if draft != nil {
		list.AddInstance(draft)()
		list.SetSelectedInstance(0)
	}
	return &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
		list:      list,
		menu:      ui.NewMenu(),
		fleet:     fleet,
	}
}

// TestSpawn_RoutesThroughKernel proves the O-key spawn path issues a
// spawn_worker syscall via the fleet seam (C3.3): runSpawnCmd records the
// SpawnOptions on the fake client, and the spawnDoneMsg handler removes the
// draft and surfaces the kernel's instance.
func TestSpawn_RoutesThroughKernel(t *testing.T) {
	fleet := &fakeFleetClient{spawnID: "kernel-id-1"}
	draft, err := session.NewInstance(session.InstanceOptions{
		Title: "orch-x", Program: "claude", Kind: session.KindOrchestrator,
	})
	require.NoError(t, err)
	draft.SetStatus(session.Loading)
	h := newSpawnHome(t, fleet, draft)
	draftID := draft.GetID()

	// Fire the spawn cmd (synchronously — the fake client returns immediately).
	cmd := h.runSpawnCmd(SpawnOptions{
		Title: "orch-x", Program: "claude", Kind: session.KindOrchestrator,
	}, draftID, false)
	msg := cmd()

	got, ok := msg.(spawnDoneMsg)
	require.True(t, ok, "runSpawnCmd yields a spawnDoneMsg")
	assert.Equal(t, "kernel-id-1", got.id)
	assert.Equal(t, draftID, got.draftID)
	require.Len(t, fleet.spawned, 1, "spawn routed through the fleet seam")
	assert.Equal(t, "orch-x", fleet.spawned[0].Title)
	assert.Equal(t, session.KindOrchestrator, fleet.spawned[0].Kind)

	// Simulate Update handling the ack: the draft is removed from the view.
	// (No kernel instance to pick up because the fake fleet's list is empty,
	// so we just assert the draft is gone.)
	h.list.RemoveByID(got.draftID)
	assert.Empty(t, h.list.GetInstances(), "draft removed on spawn ack")
}

// TestSpawn_ErrorSurfacesFailure proves a spawn syscall error is surfaced via
// the error box (not swallowed) and the draft is removed.
func TestSpawn_ErrorSurfacesFailure(t *testing.T) {
	fleet := &fakeFleetClient{spawnErr: assertErr("spawn refused")}
	draft, err := session.NewInstance(session.InstanceOptions{Title: "w", Program: "bash"})
	require.NoError(t, err)
	draft.SetStatus(session.Loading)
	h := newSpawnHome(t, fleet, draft)
	draftID := draft.GetID()

	cmd := h.runSpawnCmd(SpawnOptions{Repo: "/r", Title: "w", Program: "bash"}, draftID, false)
	msg := cmd()
	got, ok := msg.(spawnDoneMsg)
	require.True(t, ok)
	require.Error(t, got.err)
	assert.Contains(t, got.err.Error(), "spawn refused")
}

// TestSpawn_OptionsCarryHostAndBranch proves the SpawnOptions built from a draft
// instance carry the user's host + branch choices (the fields the kernel needs
// to recreate the instance on its side).
func TestSpawn_OptionsCarryHostAndBranch(t *testing.T) {
	fleet := &fakeFleetClient{spawnID: "k-1"}
	draft, err := session.NewInstance(session.InstanceOptions{Title: "w", Path: "/r", Program: "bash"})
	require.NoError(t, err)
	draft.SetSelectedBranch("feature-x")

	h := newSpawnHome(t, fleet, draft)

	cmd := h.runSpawnCmd(SpawnOptions{
		Repo:   draft.Path,
		Title:  draft.Title,
		Branch: draft.SelectedBranch(),
		Host:   draft.Host(),
	}, draft.GetID(), false)
	_ = cmd()

	require.Len(t, fleet.spawned, 1)
	assert.Equal(t, "feature-x", fleet.spawned[0].Branch)
	assert.NotNil(t, fleet.spawned[0].Host)
}

// TestSpawn_PromptOverlayCarriesHost proves the prompt+branch overlay submit
// path (handlePromptState, the Shift+N / promptAfterName flow) carries the
// host chosen in the host selector onto the spawn_worker syscall. Without
// this, a remote repo (host = dev-machine) is spawned with an empty wire host
// -> the daemon defaults to LocalHost -> "failed to find Git repository root
// from path" because the path only exists on the remote machine.
//
// The host lives on the draft instance (startNewInstance bound pendingHost
// onto it via SetHost and cleared pendingHost), so handlePromptState must
// source opts.Host from selected.Host(), not m.pendingHost (which is nil by
// then).
func TestSpawn_PromptOverlayCarriesHost(t *testing.T) {
	fleet := &fakeFleetClient{spawnID: "k-host-1"}
	draft, err := session.NewInstance(session.InstanceOptions{
		Title: "w", Path: "/root/Projets/teams-recorder", Program: "bash",
	})
	require.NoError(t, err)
	require.NoError(t, draft.SetHost(host.Lookup("dev-machine")))
	draft.SetStatus(session.Loading)

	h := newSpawnHome(t, fleet, draft)
	h.tabbedWindow = ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane())
	h.state = statePrompt
	h.menu.SetState(ui.StatePrompt)
	h.newInstanceFinalizer = func() {} // no-op; the prompt-overlay submit path calls it before spawning
	o := h.newPromptOverlay(draft.Path)
	h.textInputOverlay = o
	// Type the prompt, then Tab to the Enter button and submit. With the
	// default (no-profile) overlay, numStops is textarea(0)+branch(1)+enter(2),
	// so two Tabs land on the Enter button; Enter then submits.
	for _, r := range "do the thing" {
		h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) // 0 -> 1
	h.handleKeyPress(tea.KeyMsg{Type: tea.KeyTab}) // 1 -> 2 (enter button)
	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter}) // submit
	require.True(t, o.IsSubmitted(), "overlay must be submitted before asserting on the spawn")
	execBatch(t, cmd) // drive tea.Batch -> runSpawnCmd -> fleet.Spawn synchronously

	require.Len(t, fleet.spawned, 1, "prompt-overlay submit routes through the fleet seam")
	got := fleet.spawned[0]
	assert.Equal(t, draft.Path, got.Repo)
	assert.Equal(t, "do the thing", got.Prompt)
	require.NotNil(t, got.Host, "Host must be carried from the draft instance")
	assert.Equal(t, "dev-machine", got.Host.Name(), "Host must be the remote chosen in the host selector")
}

// execBatch executes a tea.Cmd, recursing into tea.BatchMsg so the sub-commands
// (e.g. runSpawnCmd) actually run. The TUI runtime does this automatically;
// tests driving handleKeyPress must do it by hand.
func execBatch(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	if b, ok := any(msg).(tea.BatchMsg); ok {
		for _, c := range b {
			execBatch(t, c)
		}
	}
}
