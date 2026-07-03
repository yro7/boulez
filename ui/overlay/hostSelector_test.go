package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func TestHostSelectorDefaultsToLocalWhenNoHosts(t *testing.T) {
	h := NewHostSelector(nil)
	// rows: local + (no hosts) + free = 2
	assert.Equal(t, 2, h.NumRows())
	assert.True(t, h.isLocalRow())
}

func TestHostSelectorSelectLocalByDefault(t *testing.T) {
	h := NewHostSelector([]string{"dev-machine", "gpu-box"})
	close := h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.True(t, close)
	assert.True(t, h.Submitted)
	assert.False(t, h.IsFreeAlias())
	assert.Equal(t, "local", h.SelectedAlias())
}

func TestHostSelectorNavigateAndSelectKnownAlias(t *testing.T) {
	h := NewHostSelector([]string{"dev-machine", "gpu-box"})
	// cursor 0=local, 1=dev-machine, 2=gpu-box, 3=free.
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown}) // → dev-machine
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.False(t, h.IsFreeAlias())
	assert.Equal(t, "dev-machine", h.SelectedAlias())
}

func TestHostSelectorFreeAliasTypedAndSelected(t *testing.T) {
	h := NewHostSelector([]string{"dev-machine"})
	// Navigate to the free-alias row (last row): local, dev-machine, free.
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.True(t, h.isFreeAliasRow())

	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("prod-box")})
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.True(t, h.IsFreeAlias())
	assert.Equal(t, "prod-box", h.SelectedAlias())
}

func TestHostSelectorFreeAliasBackspace(t *testing.T) {
	h := NewHostSelector(nil)
	// Move to free row (cursor 0=local, 1=free).
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("abc")})
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyBackspace})
	assert.Equal(t, "ab", h.freeAlias)
}

func TestHostSelectorTypingOnLocalRowDoesNotEditFreeAlias(t *testing.T) {
	h := NewHostSelector([]string{"dev-machine"})
	// cursor on local row (row 0).
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	assert.Equal(t, "", h.freeAlias)
}

func TestHostSelectorCancelReturnsEmpty(t *testing.T) {
	h := NewHostSelector([]string{"dev-machine"})
	close := h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.True(t, close)
	assert.True(t, h.Canceled)
	assert.Equal(t, "", h.SelectedAlias())
}

func TestHostSelectorRenderDoesNotPanic(t *testing.T) {
	h := NewHostSelector([]string{"dev-machine", "gpu-box"})
	h.SetWidth(40)
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	out := h.Render()
	assert.Contains(t, out, "Select host")
}
