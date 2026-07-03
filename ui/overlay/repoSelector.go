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

// RepoSelector is a minimal, functional overlay for choosing a repository path
// at instance creation. It offers the known repos (from the registry) plus a
// free-text path input. No visual design — that is deferred to a future TUI
// plan. The chosen path is validated by the caller (not here).
type RepoSelector struct {
	// repos is the list of known repo paths offered for selection.
	repos []string
	// cursor indexes the currently highlighted row. The last row is always the
	// free-text input; rows [0, len(repos)) are the known repos.
	cursor int
	// freePath is the text typed into the free-path input row.
	freePath string
	// width controls rendering.
	width int

	// Submitted is true after the user pressed Enter.
	Submitted bool
	// Canceled is true after the user pressed Esc.
	Canceled bool
}

// freePathRow is the index of the free-text input row (always last).
func (r *RepoSelector) freePathRow() int { return len(r.repos) }

// NumRows returns the total number of selectable rows (repos + free path).
func (r *RepoSelector) NumRows() int { return len(r.repos) + 1 }

// NewRepoSelector creates a selector pre-populated with the given known repos.
func NewRepoSelector(repos []string) *RepoSelector {
	return &RepoSelector{
		repos: repos,
		width: 60,
	}
}

// SetWidth sets the render width.
func (r *RepoSelector) SetWidth(width int) {
	if width < 20 {
		width = 20
	}
	r.width = width
}

// isFreePathRow reports whether the cursor is on the free-text input row.
func (r *RepoSelector) isFreePathRow() bool { return r.cursor == r.freePathRow() }

// move adjusts the cursor, clamping to valid rows.
func (r *RepoSelector) move(delta int) {
	n := r.NumRows()
	if n <= 0 {
		return
	}
	r.cursor = (r.cursor + delta + n) % n
}

// HandleKeyPress processes a key press. Returns true if the overlay should
// close (submit or cancel).
func (r *RepoSelector) HandleKeyPress(msg tea.KeyMsg) bool {
	switch msg.Type {
	case tea.KeyUp:
		r.move(-1)
		return false
	case tea.KeyDown:
		r.move(1)
		return false
	case tea.KeyEsc:
		r.Canceled = true
		return true
	case tea.KeyEnter:
		r.Submitted = true
		return true
	case tea.KeyBackspace:
		if r.isFreePathRow() {
			runes := []rune(r.freePath)
			if len(runes) > 0 {
				r.freePath = string(runes[:len(runes)-1])
			}
		}
		return false
	case tea.KeyRunes:
		// Only edit text when the free-path row is focused.
		if r.isFreePathRow() {
			r.freePath += string(msg.Runes)
		}
		return false
	default:
		return false
	}
}

// SelectedPath returns the path chosen by the user. For a known-repo row it is
// that repo's path; for the free-path row it is the typed text (which may be
// empty). Returns "" if nothing was selected or the overlay was canceled.
func (r *RepoSelector) SelectedPath() string {
	if r.Canceled || !r.Submitted {
		return ""
	}
	if r.isFreePathRow() {
		return strings.TrimSpace(r.freePath)
	}
	if r.cursor >= 0 && r.cursor < len(r.repos) {
		return r.repos[r.cursor]
	}
	return ""
}

// IsFreePath reports whether the selection came from the free-text input (the
// caller uses this to decide whether to register the path in the registry).
func (r *RepoSelector) IsFreePath() bool {
	return r.Submitted && !r.Canceled && r.isFreePathRow()
}

// Render renders the selector.
func (r *RepoSelector) Render() string {
	innerWidth := r.width - 6
	if innerWidth < 1 {
		innerWidth = 1
	}

	var b strings.Builder
	b.WriteString(rsTitleStyle.Render("Select repository"))
	b.WriteString("\n")

	for i, repo := range r.repos {
		line := repo
		if len(line) > innerWidth {
			line = line[:innerWidth]
		}
		if i == r.cursor {
			b.WriteString(rsSelectedStyle.Render("› " + line))
		} else {
			b.WriteString(rsNormalStyle.Render("  " + line))
		}
		b.WriteString("\n")
	}

	// Free-path input row.
	label := "Path: " + r.freePath
	cursor := ""
	if r.isFreePathRow() {
		cursor = "_"
	}
	freeLine := label + cursor
	if len(freeLine) > innerWidth {
		freeLine = freeLine[:innerWidth]
	}
	if r.isFreePathRow() {
		b.WriteString(rsSelectedStyle.Render("› " + freeLine))
	} else {
		b.WriteString(rsNormalStyle.Render("  " + freeLine))
	}
	b.WriteString("\n\n")
	b.WriteString(rsHintStyle.Render("↑↓ move · enter select · type to enter path · esc cancel"))

	return rsStyle.Render(b.String())
}
