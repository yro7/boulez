package overlay

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

func newTestRepoSelector(repos []string) *ListSelector {
	items := make([]selectorItem, 0, len(repos))
	for _, r := range repos {
		items = append(items, selectorItem{label: r, value: r, deletable: true})
	}
	return NewListSelector("Select", items, true, "Path: ", "hints")
}

func TestListSelectorCtrlDRemovesHighlightedItem(t *testing.T) {
	l := newTestRepoSelector([]string{"/a", "/b", "/c"})
	assert.Equal(t, 3, l.NumRows())
	assert.Equal(t, "/a", l.filteredItems()[l.cursor].value)

	l.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlD})

	assert.Equal(t, 2, l.NumRows())
	assert.Equal(t, []string{"/a"}, l.TakeDeletedValues())
	// Remaining items preserve order.
	assert.Equal(t, []string{"/b", "/c"}, values(l.filteredItems()))
}

func TestListSelectorCtrlDChainsAcrossMultipleItems(t *testing.T) {
	l := newTestRepoSelector([]string{"/a", "/b", "/c"})
	l.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlD})
	l.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlD})

	assert.Equal(t, 1, l.NumRows())
	assert.Equal(t, []string{"/a", "/b"}, l.TakeDeletedValues())
}

func TestListSelectorCtrlDDoesNotDeleteNonDeletableItem(t *testing.T) {
	items := []selectorItem{
		{label: "local", value: "local", deletable: false},
		{label: "dev", value: "dev", deletable: true},
	}
	l := NewListSelector("Select host", items, true, "Alias: ", "hints")
	// cursor on local (row 0).
	l.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlD})

	assert.Equal(t, 2, l.NumRows())
	assert.Empty(t, l.TakeDeletedValues())
	assert.Equal(t, "local", l.filteredItems()[l.cursor].value)
}

func TestListSelectorCtrlDClampsCursor(t *testing.T) {
	l := newTestRepoSelector([]string{"/a", "/b"})
	// Move to the last item and delete it: cursor must fall back to /a.
	l.HandleKeyPress(tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, "/b", l.filteredItems()[l.cursor].value)
	l.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlD})

	assert.Equal(t, 1, l.NumRows())
	assert.Equal(t, "/a", l.filteredItems()[l.cursor].value)
}

func TestListSelectorTakeDeletedValuesResetsAccumulator(t *testing.T) {
	l := newTestRepoSelector([]string{"/a"})
	l.HandleKeyPress(tea.KeyMsg{Type: tea.KeyCtrlD})
	assert.Equal(t, []string{"/a"}, l.TakeDeletedValues())
	assert.Empty(t, l.TakeDeletedValues())
}

func values(items []selectorItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.value)
	}
	return out
}
