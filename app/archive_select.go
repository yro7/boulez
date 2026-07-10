package app

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/ui/overlay"
)

// openArchiveSelector fetches the fleet, filters for Archived instances, and
// opens an ArchiveSelector picker listing them with a countdown until
// ReapArchived destroys them. If there are no archived instances, it is a
// no-op (nothing to restore).
func (m *home) openArchiveSelector() tea.Cmd {
	data, err := m.resolveFleet().ListInstances()
	if err != nil {
		return m.handleError(fmt.Errorf("restore: could not list instances: %w", err))
	}

	entries := make([]overlay.ArchiveEntry, 0, len(data))
	for _, d := range data {
		if d.Status != session.Archived {
			continue
		}
		entries = append(entries, overlay.ArchiveEntry{
			ID:         d.ID,
			Title:      d.Title,
			ArchivedAt: d.ArchivedAt,
		})
	}
	if len(entries) == 0 {
		return m.handleError(fmt.Errorf("no archived instances to restore"))
	}

	retention := time.Duration(m.archiveRetentionHours()) * time.Hour
	m.archiveSelector = overlay.NewArchiveSelector(entries, retention)
	m.archiveSelector.SetWidth(70)
	m.state = stateArchiveSelect
	return tea.WindowSize()
}

// handleArchiveSelectState dispatches key presses to the archive selector
// overlay and restores the selected instance on submit.
func (m *home) handleArchiveSelectState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.archiveSelector == nil {
		m.state = stateDefault
		return m, nil
	}

	if msg.String() == "ctrl+c" {
		m.archiveSelector.Canceled = true
	} else {
		shouldClose := m.archiveSelector.HandleKeyPress(msg)
		if !shouldClose {
			return m, nil
		}
	}

	if m.archiveSelector.Canceled {
		m.archiveSelector = nil
		m.state = stateDefault
		return m, tea.WindowSize()
	}

	// Submit: extract the ID from the selected value and restore it.
	id := m.archiveSelector.SelectedID()
	m.archiveSelector = nil
	m.state = stateDefault

	if id == "" {
		return m, m.handleError(fmt.Errorf("restore: no selection"))
	}

	return m, func() tea.Msg {
		if err := m.resolveFleet().Restore(id); err != nil {
			return err
		}
		_ = m.refreshFleetFromKernel()
		return instanceChangedMsg{}
	}
}
