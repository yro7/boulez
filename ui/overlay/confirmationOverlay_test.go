package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConfirmationOverlay_PropagatesCmd proves the structural fix at the root
// of the Land/Push/Kill "no feedback" bug: OnConfirm returns a tea.Cmd that
// HandleKeyPress propagates back to the caller, so the program loop can
// dispatch it and the action's outcome reaches Update. Previously OnConfirm
// was func() and the returned Cmd was discarded.
func TestConfirmationOverlay_PropagatesCmd(t *testing.T) {
	wantMsg := sentinelMsg("landed")
	called := false
	o := NewConfirmationOverlay("land?")
	o.OnConfirm = func() tea.Cmd {
		called = true
		return func() tea.Msg { return wantMsg }
	}

	closed, cmd := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	require.True(t, closed, "confirm closes the overlay")
	require.NotNil(t, cmd, "the action's Cmd must be propagated, not discarded")
	assert.True(t, called, "OnConfirm is invoked")

	// Dispatching the returned Cmd yields the action's msg — this is the
	// handoff to the Update loop that was missing before.
	msg := cmd()
	assert.Equal(t, wantMsg, msg)
}

// TestConfirmationOverlay_CancelPropagatesCmd proves the same propagation
// holds for the cancel path (esc).
func TestConfirmationOverlay_CancelPropagatesCmd(t *testing.T) {
	o := NewConfirmationOverlay("land?")
	cancelCalled := false
	o.OnCancel = func() tea.Cmd {
		cancelCalled = true
		return nil
	}

	closed, cmd := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEscape})
	require.True(t, closed)
	assert.True(t, cancelCalled)
	assert.Nil(t, cmd, "cancel returns nil Cmd by default")
}

// TestConfirmationOverlay_OtherKeysReturnNil proves non-confirm/cancel keys
// are ignored and return (false, nil) so the caller does not dispatch garbage.
func TestConfirmationOverlay_OtherKeysReturnNil(t *testing.T) {
	o := NewConfirmationOverlay("land?")
	invoked := false
	o.OnConfirm = func() tea.Cmd {
		invoked = true
		return nil
	}

	closed, cmd := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	assert.False(t, closed)
	assert.Nil(t, cmd)
	assert.False(t, invoked, "OnConfirm must not fire on unrelated keys")
}

// TestConfirmationOverlay_NilCallbacks proves missing callbacks do not panic.
func TestConfirmationOverlay_NilCallbacks(t *testing.T) {
	o := NewConfirmationOverlay("land?")

	closed, cmd := o.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	assert.True(t, closed)
	assert.Nil(t, cmd)

	o2 := NewConfirmationOverlay("land?")
	closed2, cmd2 := o2.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEscape})
	assert.True(t, closed2)
	assert.Nil(t, cmd2)
}

type sentinelMsg string

func (m sentinelMsg) String() string { return string(m) }
