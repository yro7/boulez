package app

import (
	"context"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yro7/boulez/config"
	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/ui"
)

// newRestoreHome builds a home with a fake fleet client, for testing the
// archive-selector restore flow (KeyRestore / U).
func newRestoreHome(t *testing.T, fleet fleetClient) *home {
	t.Helper()
	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	list := ui.NewList(&spin, false)
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
	_ = h.appState.SetHelpScreensSeen(allHelpScreensSeen)
	return h
}

// TestRestore_OpensArchiveSelector proves the U key opens the archive selector
// overlay when there are archived instances to restore.
func TestRestore_OpensArchiveSelector(t *testing.T) {
	fleet := &fakeFleetClient{
		list: []session.InstanceData{
			{ID: "w1", Title: "soft-deleted", Status: session.Archived},
			{ID: "w2", Title: "live", Status: session.Running},
		},
	}
	h := newRestoreHome(t, fleet)
	h.keySent = true

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("U")})

	require.Equal(t, stateArchiveSelect, h.state, "U opens the archive selector")
	require.NotNil(t, h.archiveSelector)
}

// TestRestore_NoArchivedShowsError proves the U key surfaces an error when
// there is nothing to restore, rather than opening an empty selector.
func TestRestore_NoArchivedShowsError(t *testing.T) {
	fleet := &fakeFleetClient{
		list: []session.InstanceData{
			{ID: "w1", Title: "live", Status: session.Running},
		},
	}
	h := newRestoreHome(t, fleet)
	h.keySent = true

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("U")})

	assert.NotEqual(t, stateArchiveSelect, h.state, "no archive selector opened")
	assert.Nil(t, h.archiveSelector)
	assert.NotEmpty(t, h.errBox.String(), "error surfaced to the user")
}

// TestRestore_SelectAndConfirmRestores proves selecting an archived instance
// and pressing Enter issues a Restore syscall on the kernel, returning the
// instance to the normal fleet view.
func TestRestore_SelectAndConfirmRestores(t *testing.T) {
	fleet := &fakeFleetClient{
		list: []session.InstanceData{
			{ID: "w1", Title: "soft-deleted", Status: session.Archived},
		},
	}
	h := newRestoreHome(t, fleet)
	h.keySent = true

	// Open the selector.
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("U")})
	require.Equal(t, stateArchiveSelect, h.state)
	require.NotNil(t, h.archiveSelector)

	// Submit (Enter) — the handler returns a tea.Cmd that runs Restore.
	model, cmd := h.handleArchiveSelectState(tea.KeyMsg{Type: tea.KeyEnter})
	assert.Equal(t, h, model)
	require.NotNil(t, cmd, "restore returns a Cmd to dispatch")

	// Execute the Cmd to mimic the program loop.
	_ = cmd()

	assert.Equal(t, stateDefault, h.state, "returns to default after restore")
	assert.Len(t, fleet.restored, 1, "restore syscall issued")
	assert.Equal(t, "w1", fleet.restored[0])
}

// TestRestore_EscCancels proves Esc closes the archive selector without
// issuing a restore.
func TestRestore_EscCancels(t *testing.T) {
	fleet := &fakeFleetClient{
		list: []session.InstanceData{
			{ID: "w1", Title: "soft-deleted", Status: session.Archived},
		},
	}
	h := newRestoreHome(t, fleet)
	h.keySent = true

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("U")})
	require.Equal(t, stateArchiveSelect, h.state)

	_, _ = h.handleArchiveSelectState(tea.KeyMsg{Type: tea.KeyEsc})

	assert.Equal(t, stateDefault, h.state, "Esc returns to default")
	assert.Nil(t, h.archiveSelector)
	assert.Empty(t, fleet.restored, "no restore issued on cancel")
}
