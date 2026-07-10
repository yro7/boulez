package overlay

import (
	"fmt"
	"time"
)

// ArchiveEntry is a single archived instance offered in the ArchiveSelector.
type ArchiveEntry struct {
	ID        string
	Title     string
	ArchivedAt time.Time
}

// ArchiveSelector is a thin configuration of ListSelector for choosing a
// soft-deleted (Archived) instance to restore (KeyRestore / U). Each row shows
// the instance title and a countdown until ReapArchived destroys it. Unlike
// HostSelector/RepoSelector, there is no free-text input: the user must pick
// an existing archived instance. All list behaviour (cursor, typing to filter,
// submit, cancel) lives in the embedded ListSelector.
type ArchiveSelector struct {
	*ListSelector
}

// archiveHints is the help line shown at the bottom of the archive selector.
const archiveHints = "type to filter · ↑↓ move · enter restore · esc cancel"

// NewArchiveSelector creates a selector pre-populated with the given archived
// instances. retention is the retention window used to compute each row's
// countdown label.
func NewArchiveSelector(entries []ArchiveEntry, retention time.Duration) *ArchiveSelector {
	items := make([]selectorItem, 0, len(entries))
	for _, e := range entries {
		items = append(items, selectorItem{
			label: archiveRowLabel(e, retention),
			value: e.ID,
		})
	}
	return &ArchiveSelector{
		ListSelector: NewListSelector("Restore archived instance", items, false, "", archiveHints),
	}
}

// SelectedID returns the instance ID chosen by the user, or "" if nothing was
// selected or the overlay was canceled.
func (a *ArchiveSelector) SelectedID() string { return a.SelectedValue() }

// archiveRowLabel renders an archived instance's row: title + countdown until
// ReapArchived destroys it.
func archiveRowLabel(e ArchiveEntry, retention time.Duration) string {
	var remaining time.Duration
	if e.ArchivedAt.IsZero() {
		remaining = retention
	} else {
		remaining = retention - time.Since(e.ArchivedAt)
	}
	if remaining < 0 {
		remaining = 0
	}
	hours := int(remaining.Hours())
	return fmt.Sprintf("%s — expires in %dh", e.Title, hours)
}
