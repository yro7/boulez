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

	rsDimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	rsHintStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	rsFilterStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("7"))
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
//
// It is an fzf-style "filter or create" picker: a single text input (the
// filter) narrows the known items live; Enter selects the highlighted match,
// or — when the filter matches nothing — creates a free-text value from the
// filter itself (e.g. a new repo path or ssh alias). There is no separate
// free-text row: the filter doubles as the entry field.
//
// HostSelector and RepoSelector embed it and add only selector-specific
// accessors. This avoids duplicating the filter/delete logic across two
// near-identical selectors.
type ListSelector struct {
	items []selectorItem
	// cursor indexes into filteredItems(), the currently highlighted match.
	cursor int
	// filter is the live text the user types. It narrows the items and, when
	// it matches nothing, becomes the free-text value on Enter.
	filter string
	// allowFree controls whether a non-matching filter can be submitted as a
	// free-text value (repos/hosts: yes; a pure picker would say no).
	allowFree bool
	// freeLabel is the prefix shown before the filter (e.g. "Path: "), also
	// hinting what a non-matching filter becomes.
	freeLabel string
	title     string
	hints     string
	width     int

	// Submitted is true after the user pressed Enter on a real selection
	// (item match or free-text value).
	Submitted bool
	// Canceled is true after the user pressed Esc.
	Canceled bool
	// selectedFree records that the selection came from the free-text filter
	// (no item matched), so the caller can register it.
	selectedFree bool

	// deleted accumulates the values of items removed via ctrl+d this session.
	// The caller reads it via DeletedValues() to apply Registry.Remove.
	deleted []string
}

// NewListSelector creates a selector with the given items and an optional
// free-text filter (allowFree controls whether a non-matching filter can be
// submitted as a value).
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

// SetItems replaces the offered items, preserving the cursor and filter when
// possible. Cursor is clamped to the filtered range. Used to narrow the list
// after an async host-aware filter lands.
func (l *ListSelector) SetItems(items []selectorItem) {
	l.items = items
	l.clampCursor()
}

// SetWidth sets the render width.
func (l *ListSelector) SetWidth(width int) {
	if width < 20 {
		width = 20
	}
	l.width = width
}

// filteredItems returns the items whose label contains the filter
// (case-insensitive substring match). An empty filter returns all items.
func (l *ListSelector) filteredItems() []selectorItem {
	if l.filter == "" {
		return l.items
	}
	needle := strings.ToLower(l.filter)
	out := make([]selectorItem, 0, len(l.items))
	for _, it := range l.items {
		if strings.Contains(strings.ToLower(it.label), needle) {
			out = append(out, it)
		}
	}
	return out
}

// clampCursor ensures the cursor is within the filtered range. When the filter
// changes, the previously highlighted item may no longer be visible; we keep
// the cursor on the same index if still valid, otherwise reset to the top.
func (l *ListSelector) clampCursor() {
	n := len(l.filteredItems())
	if n == 0 {
		l.cursor = 0
		return
	}
	if l.cursor >= n {
		l.cursor = n - 1
	}
	if l.cursor < 0 {
		l.cursor = 0
	}
}

// NumRows returns the number of currently visible (filtered) rows. (The filter
// input is always shown but is not a selectable row.)
func (l *ListSelector) NumRows() int { return len(l.filteredItems()) }

// isFreeRow is kept for compatibility; in the fused model the filter line is
// always "the free row". It reports whether a submit would resolve to the
// free-text filter (i.e. no item is highlighted).
func (l *ListSelector) isFreeRow() bool { return len(l.filteredItems()) == 0 }

// move adjusts the cursor among the filtered items, clamping at the ends.
func (l *ListSelector) move(delta int) {
	n := len(l.filteredItems())
	if n == 0 {
		return
	}
	l.cursor += delta
	if l.cursor < 0 {
		l.cursor = 0
	}
	if l.cursor >= n {
		l.cursor = n - 1
	}
}

// HandleKeyPress processes a key press. Returns true if the overlay should
// close (submit or cancel).
//
// Typing always edits the filter (there is no row to focus). ↑↓ move the
// highlight among the filtered matches. Enter selects the highlighted match,
// or — when nothing matches and free-text is allowed — submits the filter as a
// free-text value. When nothing matches and free-text is not allowed (or the
// filter is empty), Enter closes without submitting so the caller can show an
// error.
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
		return l.submit()
	case tea.KeyBackspace:
		if len(l.filter) > 0 {
			runes := []rune(l.filter)
			l.filter = string(runes[:len(runes)-1])
			l.clampCursor()
		}
		return false
	case tea.KeySpace:
		l.filter += " "
		l.clampCursor()
		return false
	case tea.KeyRunes:
		l.filter += string(msg.Runes)
		l.clampCursor()
		return false
	default:
		return false
	}
}

// submit resolves an Enter press. Returns true (close) whenever there is
// something to act on — a real selection, a free-text value, or a nothing-
// matched state that the caller will turn into an error.
func (l *ListSelector) submit() bool {
	items := l.filteredItems()
	if len(items) > 0 && l.cursor < len(items) {
		// Highlighted match: select it.
		l.Submitted = true
		l.selectedFree = false
		return true
	}
	if l.allowFree && strings.TrimSpace(l.filter) != "" {
		// No match, but the filter is a usable free-text value.
		l.Submitted = true
		l.selectedFree = true
		return true
	}
	// Nothing to select (empty filter, no items). Close without submitting so
	// the caller shows a "please select / type a value" error.
	l.Submitted = false
	return true
}

// SelectedValue returns the value chosen by the user. For a filtered match it
// is that item's value; for a free-text submit it is the trimmed filter. Returns
// "" if nothing was selected or the overlay was canceled.
func (l *ListSelector) SelectedValue() string {
	if l.Canceled || !l.Submitted {
		return ""
	}
	if l.selectedFree {
		return strings.TrimSpace(l.filter)
	}
	items := l.filteredItems()
	if l.cursor >= 0 && l.cursor < len(items) {
		return items[l.cursor].value
	}
	return ""
}

// IsFreeValue reports whether the selection came from the free-text filter (the
// caller uses this to decide whether to register the value in the registry).
func (l *ListSelector) IsFreeValue() bool {
	return l.Submitted && !l.Canceled && l.selectedFree
}

// DeletedValues returns the values of items removed via ctrl+d this session.
// The caller applies Registry.Remove for each, best-effort.
func (l *ListSelector) DeletedValues() []string {
	return l.deleted
}

// Render renders the selector: a filter input line (always visible) followed
// by the filtered matches, with the highlighted match marked.
func (l *ListSelector) Render() string {
	innerWidth := l.width - 6
	if innerWidth < 1 {
		innerWidth = 1
	}

	var b strings.Builder
	b.WriteString(rsTitleStyle.Render(l.title))
	b.WriteString("\n")

	// Filter input line (doubles as free-text entry). A block cursor marks
	// the editable end.
	filterLine := l.freeLabel + l.filter + "█"
	if len(filterLine) > innerWidth {
		filterLine = filterLine[:innerWidth]
	}
	b.WriteString(rsFilterStyle.Render(filterLine))
	b.WriteString("\n")

	// Filtered matches.
	items := l.filteredItems()
	if len(items) == 0 {
		noun := strings.TrimRight(l.freeLabel, " ")
		hint := "No matches — enter to use as a new " + noun
		b.WriteString(rsDimStyle.Render(hint))
	} else {
		// Window the list around the cursor (max 8 visible) so long lists
		// stay navigable.
		maxVisible := 8
		start := 0
		if l.cursor >= maxVisible {
			start = l.cursor - maxVisible + 1
		}
		end := start + maxVisible
		if end > len(items) {
			end = len(items)
		}
		for i := start; i < end; i++ {
			line := items[i].label
			if len(line) > innerWidth {
				line = line[:innerWidth]
			}
			if i == l.cursor {
				b.WriteString(rsSelectedStyle.Render("› " + line))
			} else {
				b.WriteString(rsNormalStyle.Render("  " + line))
			}
			if i < end-1 {
				b.WriteString("\n")
			}
		}
	}
	b.WriteString("\n\n")
	b.WriteString(rsHintStyle.Render(l.hints))

	return rsStyle.Render(b.String())
}
