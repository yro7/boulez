package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yro7/boulez/config"
	"github.com/yro7/boulez/session"
)

// TestKeyEnter_PreviewTabReturnsAttachCmd proves the Preview-tab attach path
// returns a non-nil tea.Cmd (the tea.ExecProcess attach command) instead of
// the old blocking <-ch inside Update. With all help screens marked seen,
// showHelpScreen skips display and fires the dismiss callback immediately, so
// the returned Cmd is the tea.ExecProcess wrapping host.AttachCmd. This is
// the regression guard for the SSH attach freeze: the path must NOT block in
// Update — it must hand back a Cmd.
func TestKeyEnter_PreviewTabReturnsAttachCmd(t *testing.T) {
	inst, _ := newStartedMockInstance(t, session.Ready)
	h := newInsertTestHome(t, inst)
	h.keySent = true // bypass menu-highlight early-return
	// Mark all help screens seen so showHelpScreen fires onDismiss immediately
	// (returns the attach Cmd) rather than entering stateHelp.
	h.appState = config.LoadState()
	_ = h.appState.SetHelpScreensSeen(allHelpScreensSeen)

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	require.NotNil(t, cmd, "KeyEnter on a Ready Preview-tab instance must return an attach Cmd, not nil")
	assert.NotEqual(t, stateHelp, h.state, "help overlay must be skipped (already seen)")
}

// TestKeyEnter_PausedInstanceReturnsNil proves the guard: a paused instance
// does not trigger an attach.
func TestKeyEnter_PausedInstanceReturnsNil(t *testing.T) {
	inst, _ := newStartedMockInstance(t, session.Paused)
	h := newInsertTestHome(t, inst)
	h.keySent = true
	h.appState = config.LoadState()
	_ = h.appState.SetHelpScreensSeen(allHelpScreensSeen)

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Nil(t, cmd, "KeyEnter on a paused instance must not attach")
}

// TestKeyEnter_TerminalTabNoSessionReturnsError proves the Terminal-tab path
// surfaces a clean error (as a Cmd) when no terminal session exists yet,
// rather than crashing or blocking. This guards the tea.ExecProcess wiring:
// the path must build a local attach cmd only when a session name is present.
func TestKeyEnter_TerminalTabNoSessionReturnsError(t *testing.T) {
	inst, _ := newStartedMockInstance(t, session.Ready)
	h := newInsertTestHome(t, inst)
	h.keySent = true
	h.appState = config.LoadState()
	_ = h.appState.SetHelpScreensSeen(allHelpScreensSeen)
	// Switch to the Terminal tab. No terminal tmux session has been created,
	// so TerminalSessionName() returns "".
	h.tabbedWindow.Toggle() // Preview → Diff
	h.tabbedWindow.Toggle() // Diff → Terminal
	require.True(t, h.tabbedWindow.IsInTerminalTab())

	_, cmd := h.handleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd, "KeyEnter on the Terminal tab with no session must return an error Cmd")
}
