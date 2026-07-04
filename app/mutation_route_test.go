package app

import (
	"context"
	"testing"

	"claude-squad/config"
	"claude-squad/session"
	"claude-squad/ui"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allHelpScreensSeen is a bitmask with every help screen's bit set, so
// showHelpScreen skips display and runs the onDismiss callback immediately
// (tests don't render the help overlay).
const allHelpScreensSeen = ^uint32(0)

// newMutationHome builds a home with a fake fleet client and a single
// Ready instance selected, for testing pause/resume/kill routing (C3.4).
func newMutationHome(t *testing.T, fleet fleetClient, inst *session.Instance) *home {
	t.Helper()
	spin := spinner.Model{}
	list := ui.NewList(&spin, false)
	list.AddInstance(inst)()
	list.SetSelectedInstance(0)
	h := &home{
		ctx:          context.Background(),
		state:        stateDefault,
		appConfig:    config.DefaultConfig(),
		appState:     config.LoadState(),
		list:         list,
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane()),
		errBox:       ui.NewErrBox(),
		fleet:        fleet,
	}
	// Mark all help screens seen so showHelpScreen callbacks fire immediately.
	_ = h.appState.SetHelpScreensSeen(allHelpScreensSeen)
	return h
}

func newReadyInstance(t *testing.T, title string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: title, Path: t.TempDir(), Program: "bash",
	})
	require.NoError(t, err)
	inst.SetStatus(session.Ready)
	return inst
}

// TestResume_RoutesThroughKernel proves the R key issues a Resume syscall on
// the selected instance's ID (C3.4).
func TestResume_RoutesThroughKernel(t *testing.T) {
	fleet := &fakeFleetClient{}
	inst := newReadyInstance(t, "w1")
	h := newMutationHome(t, fleet, inst)
	h.keySent = true // bypass menu-highlight early-return

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})

	require.Len(t, fleet.resumed, 1, "resume routed through the kernel")
	assert.Equal(t, inst.GetID(), fleet.resumed[0])
	assert.Empty(t, fleet.paused, "pause not triggered")
}

// TestPause_RoutesThroughKernel proves the checkout key issues a Pause syscall
// on the selected instance's ID (C3.4). The checkout shows a help screen whose
// callback runs the pause; with all help screens marked seen, the callback
// fires immediately.
func TestPause_RoutesThroughKernel(t *testing.T) {
	fleet := &fakeFleetClient{}
	inst := newReadyInstance(t, "w1")
	h := newMutationHome(t, fleet, inst)
	h.keySent = true

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})

	require.Len(t, fleet.paused, 1, "pause routed through the kernel")
	assert.Equal(t, inst.GetID(), fleet.paused[0])
}

// TestKill_GuardShortCircuitsOnNoWorktree proves the kill action runs (the
// routing is wired) but short-circuits at the GetGitWorktree pre-flight when
// the instance has no started worktree — no syscall is issued. This is the
// defense-in-depth guard living in the TUI before the kernel syscall.
func TestKill_GuardShortCircuitsOnNoWorktree(t *testing.T) {
	fleet := &fakeFleetClient{}
	inst := newReadyInstance(t, "w1")
	h := newMutationHome(t, fleet, inst)
	h.keySent = true

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("D")})
	require.Equal(t, stateConfirm, h.state, "D opens the confirmation modal")
	require.NotNil(t, h.confirmationOverlay)
	// Confirm runs the killAction; with no started worktree it errors at
	// GetGitWorktree before reaching the syscall.
	h.confirmationOverlay.OnConfirm()

	assert.Empty(t, fleet.killed, "kill guard short-circuits before the syscall")
}
