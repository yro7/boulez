package overlay

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// HostSelector is a minimal, functional overlay for choosing an execution
// host (local or a known ssh alias) at instance creation. Mirrors RepoSelector.
// The "local" host is always offered as the first row; the free-text input
// (last row) accepts an ssh alias not yet in the registry. The chosen alias is
// validated/reached by the caller (not here).
type HostSelector struct {
	// hosts is the list of known ssh aliases offered for selection (from the
	// host registry). "local" is always prepended and is not in this slice.
	hosts []string
	// cursor indexes the currently highlighted row. Row 0 is "local"; rows
	// [1, len(hosts)] are the known aliases; the last row is the free-text
	// input.
	cursor int
	// freeAlias is the text typed into the free-text input row.
	freeAlias string
	// width controls rendering.
	width int

	// Submitted is true after the user pressed Enter.
	Submitted bool
	// Canceled is true after the user pressed Esc.
	Canceled bool
}

// localLabel is the label shown for the implicit local host row.
const localLabel = "local"

// freeAliasRow is the index of the free-text input row (always last).
func (h *HostSelector) freeAliasRow() int { return len(h.hosts) + 1 }

// NumRows returns the total number of selectable rows (local + hosts + free).
func (h *HostSelector) NumRows() int { return len(h.hosts) + 2 }

// NewHostSelector creates a selector pre-populated with the given known ssh
// aliases (local is always prepended as the first row).
func NewHostSelector(hosts []string) *HostSelector {
	return &HostSelector{
		hosts: hosts,
		width: 60,
	}
}

// SetWidth sets the render width.
func (h *HostSelector) SetWidth(width int) {
	if width < 20 {
		width = 20
	}
	h.width = width
}

// isFreeAliasRow reports whether the cursor is on the free-text input row.
func (h *HostSelector) isFreeAliasRow() bool { return h.cursor == h.freeAliasRow() }

// isLocalRow reports whether the cursor is on the "local" row (row 0).
func (h *HostSelector) isLocalRow() bool { return h.cursor == 0 }

// move adjusts the cursor, clamping to valid rows.
func (h *HostSelector) move(delta int) {
	n := h.NumRows()
	if n <= 0 {
		return
	}
	h.cursor = (h.cursor + delta + n) % n
}

// HandleKeyPress processes a key press. Returns true if the overlay should
// close (submit or cancel).
func (h *HostSelector) HandleKeyPress(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyUp:
		h.move(-1)
		return false
	case tea.KeyDown:
		h.move(1)
		return false
	case tea.KeyEsc:
		h.Canceled = true
		return true
	case tea.KeyEnter:
		h.Submitted = true
		return true
	case tea.KeyBackspace:
		if h.isFreeAliasRow() {
			runes := []rune(h.freeAlias)
			if len(runes) > 0 {
				h.freeAlias = string(runes[:len(runes)-1])
			}
		}
		return false
	case tea.KeyRunes:
		// Only edit text when the free-text row is focused.
		if h.isFreeAliasRow() {
			h.freeAlias += string(msg.Runes)
		}
		return false
	default:
		return false
	}
}

// SelectedAlias returns the alias chosen by the user. "local" for the local
// row; the alias for a known-host row; the typed text for the free-text row
// (which may be empty). Returns "" if nothing was selected or the overlay was
// canceled.
func (h *HostSelector) SelectedAlias() string {
	if h.Canceled || !h.Submitted {
		return ""
	}
	if h.isLocalRow() {
		return localLabel
	}
	if h.isFreeAliasRow() {
		return strings.TrimSpace(h.freeAlias)
	}
	// Known-host rows are at cursor 1..len(hosts).
	idx := h.cursor - 1
	if idx >= 0 && idx < len(h.hosts) {
		return h.hosts[idx]
	}
	return ""
}

// IsFreeAlias reports whether the selection came from the free-text input (the
// caller uses this to decide whether to register the alias in the registry).
func (h *HostSelector) IsFreeAlias() bool {
	return h.Submitted && !h.Canceled && h.isFreeAliasRow()
}

// Render renders the selector.
func (h *HostSelector) Render() string {
	innerWidth := h.width - 6
	if innerWidth < 1 {
		innerWidth = 1
	}

	var b strings.Builder
	b.WriteString(rsTitleStyle.Render("Select host"))
	b.WriteString("\n")

	// Local row (always first).
	localLine := localLabel + "  (this machine)"
	if len(localLine) > innerWidth {
		localLine = localLine[:innerWidth]
	}
	if h.isLocalRow() {
		b.WriteString(rsSelectedStyle.Render("› " + localLine))
	} else {
		b.WriteString(rsNormalStyle.Render("  " + localLine))
	}
	b.WriteString("\n")

	// Known aliases.
	for _, alias := range h.hosts {
		line := alias
		if len(line) > innerWidth {
			line = line[:innerWidth]
		}
		idx := h.cursor - 1
		if idx >= 0 && idx < len(h.hosts) && alias == h.hosts[idx] {
			b.WriteString(rsSelectedStyle.Render("› " + line))
		} else {
			b.WriteString(rsNormalStyle.Render("  " + line))
		}
		b.WriteString("\n")
	}

	// Free-alias input row.
	label := "Alias: " + h.freeAlias
	cursor := ""
	if h.isFreeAliasRow() {
		cursor = "_"
	}
	freeLine := label + cursor
	if len(freeLine) > innerWidth {
		freeLine = freeLine[:innerWidth]
	}
	if h.isFreeAliasRow() {
		b.WriteString(rsSelectedStyle.Render("› " + freeLine))
	} else {
		b.WriteString(rsNormalStyle.Render("  " + freeLine))
	}
	b.WriteString("\n\n")
	b.WriteString(rsHintStyle.Render("↑↓ move · enter select · type to enter alias · esc cancel"))

	return rsStyle.Render(b.String())
}
