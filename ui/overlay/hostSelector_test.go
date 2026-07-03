package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func TestHostSelectorSelectsLocalByDefaultWhenNoFilter(t *testing.T) {
	h := NewHostSelector([]string{"dev-machine", "gpu-box"})
	// Empty filter → local is the first item and is highlighted by default.
	assert.True(t, h.isLocalRow())

	close := h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.True(t, close)
	assert.True(t, h.Submitted)
	assert.False(t, h.IsFreeAlias())
	assert.Equal(t, "local", h.SelectedAlias())
}

func TestHostSelectorNavigateAndSelectKnownAlias(t *testing.T) {
	h := NewHostSelector([]string{"dev-machine", "gpu-box"})
	// cursor starts on local (filtered row 0); move down to dev-machine.
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.False(t, h.IsFreeAlias())
	assert.Equal(t, "dev-machine", h.SelectedAlias())
}

func TestHostSelectorFilterNarrowsAndSelectsMatch(t *testing.T) {
	h := NewHostSelector([]string{"dev-machine", "gpu-box"})
	// Type a filter that matches only dev-machine.
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("dev")})
	assert.Equal(t, 1, h.NumRows()) // only dev-machine matches
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.False(t, h.IsFreeAlias())
	assert.Equal(t, "dev-machine", h.SelectedAlias())
}

func TestHostSelectorFreeAliasTypedWhenNoMatch(t *testing.T) {
	h := NewHostSelector([]string{"dev-machine"})
	// Type an alias that matches nothing → Enter submits it as a free alias.
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("prod-box")})
	assert.Equal(t, 0, h.NumRows()) // no matches
	assert.True(t, h.isFreeAliasRow())

	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.True(t, h.IsFreeAlias())
	assert.Equal(t, "prod-box", h.SelectedAlias())
}

func TestHostSelectorFilterBackspace(t *testing.T) {
	h := NewHostSelector(nil)
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("abc")})
	h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyBackspace})
	assert.Equal(t, "ab", h.filter)
}

func TestHostSelectorEnterWithEmptyFilterSelectsLocal(t *testing.T) {
	h := NewHostSelector(nil)
	// No registered aliases, but "local" is always present. Empty filter →
	// local is the highlighted match and Enter selects it.
	close := h.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.True(t, close)
	assert.True(t, h.Submitted)
	assert.Equal(t, "local", h.SelectedAlias())
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
