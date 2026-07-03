package overlay

import (
	"testing"

	"github.com/stretchr/testify/assert"
	tea "github.com/charmbracelet/bubbletea"
)

func TestRepoSelectorDefaultsToFreePathRowWhenNoRepos(t *testing.T) {
	r := NewRepoSelector(nil)
	assert.Equal(t, 1, r.NumRows())
	assert.True(t, r.isFreePathRow())
}

func TestRepoSelectorSelectKnownRepo(t *testing.T) {
	r := NewRepoSelector([]string{"/a", "/b"})
	// cursor starts at 0 → first repo.
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

func TestRepoSelectorFreePathTypedAndSelected(t *testing.T) {
	r := NewRepoSelector([]string{"/a"})
	// Move to the free-path row (last row).
	r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.True(t, r.isFreePathRow())

	r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/some/path")})
	r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyEnter})

	assert.True(t, r.IsFreePath())
	assert.Equal(t, "/some/path", r.SelectedPath())
}

func TestRepoSelectorFreePathBackspace(t *testing.T) {
	r := NewRepoSelector(nil)
	r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("abc")})
	r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyBackspace})
	assert.Equal(t, "ab", r.freePath)
}

func TestRepoSelectorTypingOnRepoRowDoesNotEditFreePath(t *testing.T) {
	r := NewRepoSelector([]string{"/a"})
	// cursor on repo row.
	r.HandleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	assert.Equal(t, "", r.freePath)
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
