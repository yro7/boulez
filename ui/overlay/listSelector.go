package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	rsStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(1, 2)

	rsTitleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("62")).
			Bold(true).
			MarginBottom(1)

	rsSelectedStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("62")).
			Foreground(lipgloss.Color("0"))

	rsNormalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("7"))

	rsHintStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))
)

// selectorItem is a single selectable row in a ListSelector.
type selectorItem struct {
	// label is the text shown for the row.
	label string
	// value is the value returned on selection (may differ from label).
	value string
	// deletable reports whether ctrl+d can remove this row from the list.
	// Implicit entries (e.g. "local") are non-deletable.
	deletable bool
}

// ListSelector is the shared, deep module behind HostSelector and RepoSelector.
// It renders a list of items plus an optional free-text input row, and handles
// cursor movement, typing (on the free row), submission and cancellation.
// HostSelector and RepoSelector are thin configurations of this module: they
// embed it and add only selector-specific accessors. This avoids duplicating
// the list/filter/delete logic across two near-identical selectors.
type ListSelector struct {
	items []selectorItem
	// cursor indexes the currently highlighted row. The free-text row (when
	// allowFree) is always the last row.
	cursor    int
	freeText  string
	allowFree bool
	// freeLabel is the prefix shown before the free text (e.g. "Path: ").
	freeLabel string
	title     string
	hints     string
	width     int

	// Submitted is true after the user pressed Enter.
	Submitted bool
	// Canceled is true after the user pressed Esc.
	Canceled bool

	// deleted accumulates the values of items removed via ctrl+d this session.
	// The caller reads it via DeletedValues() to apply Registry.Remove.
	deleted []string
}

// NewListSelector creates a selector with the given items and an optional
// free-text input row.
func NewListSelector(title string, items []selectorItem, allowFree bool, freeLabel, hints string) *ListSelector {
	return &ListSelector{
		items:     items,
		allowFree: allowFree,
		freeLabel: freeLabel,
		title:     title,
		hints:     hints,
		width:     60,
	}
}

// SetItems replaces the offered items, preserving the cursor and free-text
// input when possible. Cursor is clamped to the free row (last) if it now
// points past the end. Used to narrow the list after an async host-aware
// filter lands.
func (l *ListSelector) SetItems(items []selectorItem) {
	l.items = items
	if l.cursor > l.freeRow() {
		l.cursor = l.freeRow()
	}
}

// SetWidth sets the render width.
func (l *ListSelector) SetWidth(width int) {
	if width < 20 {
		width = 20
	}
	l.width = width
}

// freeRow is the index of the free-text input row (always last). Returns
// len(items) when allowFree; otherwise there is no free row and this returns
// len(items) (== NumRows, i.e. one past the last item), which is only used as
// a clamp sentinel.
func (l *ListSelector) freeRow() int { return len(l.items) }

// NumRows returns the total number of selectable rows (items + free row).
func (l *ListSelector) NumRows() int {
	if l.allowFree {
		return len(l.items) + 1
	}
	return len(l.items)
}

// isFreeRow reports whether the cursor is on the free-text input row.
func (l *ListSelector) isFreeRow() bool { return l.allowFree && l.cursor == l.freeRow() }

// move adjusts the cursor, clamping to valid rows with wraparound.
func (l *ListSelector) move(delta int) {
	n := l.NumRows()
	if n <= 0 {
		return
	}
	l.cursor = (l.cursor + delta + n) % n
}

// HandleKeyPress processes a key press. Returns true if the overlay should
// close (submit or cancel).
func (l *ListSelector) HandleKeyPress(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyUp:
		l.move(-1)
		return false
	case tea.KeyDown:
		l.move(1)
		return false
	case tea.KeyEsc:
		l.Canceled = true
		return true
	case tea.KeyEnter:
		l.Submitted = true
		return true
	case tea.KeyBackspace:
		if l.isFreeRow() {
			runes := []rune(l.freeText)
			if len(runes) > 0 {
				l.freeText = string(runes[:len(runes)-1])
			}
		}
		return false
	case tea.KeyRunes:
		// Only edit text when the free-text row is focused.
		if l.isFreeRow() {
			l.freeText += string(msg.Runes)
		}
		return false
	default:
		return false
	}
}

// SelectedValue returns the value chosen by the user. For an item row it is
// that item's value; for the free-text row it is the typed text (which may be
// empty). Returns "" if nothing was selected or the overlay was canceled.
func (l *ListSelector) SelectedValue() string {
	if l.Canceled || !l.Submitted {
		return ""
	}
	if l.isFreeRow() {
		return strings.TrimSpace(l.freeText)
	}
	if l.cursor >= 0 && l.cursor < len(l.items) {
		return l.items[l.cursor].value
	}
	return ""
}

// IsFreeValue reports whether the selection came from the free-text input (the
// caller uses this to decide whether to register the value in the registry).
func (l *ListSelector) IsFreeValue() bool {
	return l.Submitted && !l.Canceled && l.isFreeRow()
}

// DeletedValues returns the values of items removed via ctrl+d this session.
// The caller applies Registry.Remove for each, best-effort.
func (l *ListSelector) DeletedValues() []string {
	return l.deleted
}

// Render renders the selector.
func (l *ListSelector) Render() string {
	innerWidth := l.width - 6
	if innerWidth < 1 {
		innerWidth = 1
	}

	var b strings.Builder
	b.WriteString(rsTitleStyle.Render(l.title))
	b.WriteString("\n")

	for i, item := range l.items {
		line := item.label
		if len(line) > innerWidth {
			line = line[:innerWidth]
		}
		if i == l.cursor {
			b.WriteString(rsSelectedStyle.Render("› " + line))
		} else {
			b.WriteString(rsNormalStyle.Render("  " + line))
		}
		b.WriteString("\n")
	}

	// Free-text input row.
	if l.allowFree {
		label := l.freeLabel + l.freeText
		cursor := ""
		if l.isFreeRow() {
			cursor = "_"
		}
		freeLine := label + cursor
		if len(freeLine) > innerWidth {
			freeLine = freeLine[:innerWidth]
		}
		if l.isFreeRow() {
			b.WriteString(rsSelectedStyle.Render("› " + freeLine))
		} else {
			b.WriteString(rsNormalStyle.Render("  " + freeLine))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(rsHintStyle.Render(l.hints))

	return rsStyle.Render(b.String())
}
