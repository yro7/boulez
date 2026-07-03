package overlay

import (
	"testing"

	"github.com/stretchr/testify/assert"
	tea "github.com/charmbracelet/bubbletea"
)

func TestRepoSelectorSelectsFirstRepoByDefault(t *testing.T) {
	r := NewRepoSelector([]string{"/a", "/b"})
	// Empty filter → first repo is highlighted.
	close := r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.True(t, close)
	assert.True(t, r.Submitted)
	assert.False(t, r.IsFreePath())
	assert.Equal(t, "/a", r.SelectedPath())
}

func TestRepoSelectorNavigateAndSelectSecondRepo(t *testing.T) {
	r := NewRepoSelector([]string{"/a", "/b"})
	r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.False(t, r.IsFreePath())
	assert.Equal(t, "/b", r.SelectedPath())
}

func TestRepoSelectorFilterNarrowsAndSelectsMatch(t *testing.T) {
	r := NewRepoSelector([]string{"/a", "/b", "/c"})
	r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b")})
	assert.Equal(t, 1, r.NumRows())
	r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.False(t, r.IsFreePath())
	assert.Equal(t, "/b", r.SelectedPath())
}

func TestRepoSelectorFreePathTypedWhenNoMatch(t *testing.T) {
	r := NewRepoSelector([]string{"/a"})
	// Type a path that matches nothing → Enter submits it as a free path.
	r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/some/path")})
	assert.Equal(t, 0, r.NumRows())
	assert.True(t, r.isFreePathRow())

	r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.True(t, r.IsFreePath())
	assert.Equal(t, "/some/path", r.SelectedPath())
}

func TestRepoSelectorFilterBackspace(t *testing.T) {
	r := NewRepoSelector(nil)
	r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("abc")})
	r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyBackspace})
	assert.Equal(t, "ab", r.filter)
}

func TestRepoSelectorEnterWithEmptyFilterAndNoItemsDoesNotSubmit(t *testing.T) {
	r := NewRepoSelector(nil)
	close := r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})
	assert.True(t, close)
	assert.False(t, r.Submitted)
	assert.Equal(t, "", r.SelectedPath())
}

func TestRepoSelectorCancelReturnsEmpty(t *testing.T) {
	r := NewRepoSelector([]string{"/a"})
	close := r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEsc})
	assert.True(t, close)
	assert.True(t, r.Canceled)
	assert.Equal(t, "", r.SelectedPath())
}

func TestRepoSelectorRenderDoesNotPanic(t *testing.T) {
	r := NewRepoSelector([]string{"/a", "/b"})
	r.SetWidth(40)
	r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	out := r.Render()
	assert.Contains(t, out, "Select repository")
}
