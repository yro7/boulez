package app

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yro7/boulez/host"
	"github.com/yro7/boulez/ui/overlay"
)

// This file groups the host-selector overlay lifecycle: opening it (with the
// 0/1/≥2 alias skip logic), dispatching key presses while it is open, and
// persisting ctrl+d removals to the host registry. Extracted from app.go so
// the host-selection flow lives in one place, mirroring prompt_state.go /
// new_state.go.

// openHostSelector opens the host selector overlay first, before the repo
// selector. promptFlow controls whether the prompt+branch overlay follows name
// entry (KeyPrompt) or not (KeyNew). The chosen host is stashed in pendingHost
// and carried into the repo selector.
func (m *home) openHostSelector(promptFlow bool) tea.Cmd {
	var aliases []string
	if m.hostRegistry != nil {
		aliases, _ = m.hostRegistry.List()
	}
	// Skip the selector when there is no real choice: 0 registered aliases
	// → local (always implicit), 1 alias → that alias. The selector only opens
	// when there are ≥2 options (local + ≥1 alias, or ≥2 aliases). Avoids a
	// gratuitous Enter in the common all-local case.
	if len(aliases) < 2 {
		alias := host.LocalAlias
		if len(aliases) == 1 {
			alias = aliases[0]
		}
		m.repoSelectPrompt = promptFlow
		m.pendingHost = host.Lookup(alias)
		return m.openRepoSelector(promptFlow)
	}
	m.hostSelector = overlay.NewHostSelector(aliases)
	m.repoSelectPrompt = promptFlow
	m.state = stateHostSelect
	return tea.WindowSize()
}

// handleHostSelectState dispatches key presses to the host selector overlay and
// finalizes the selection on submit/cancel, transitioning to the repo selector.
func (m *home) handleHostSelectState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.hostSelector == nil {
		m.state = stateDefault
		return m, nil
	}

	// ctrl+c cancels like esc.
	if msg.String() == "ctrl+c" {
		m.hostSelector.Canceled = true
	} else {
		shouldClose := m.hostSelector.HandleKeyPress(msg)
		// Apply silent ctrl+d removals to the persistent host registry. Done
		// on every non-close keypress (TakeDeletedValues is a no-op when
		// nothing was deleted); local is protected inside the selector.
		m.applyHostDeletions()
		if !shouldClose {
			return m, nil
		}
	}

	if m.hostSelector.Canceled {
		m.hostSelector = nil
		m.state = stateDefault
		return m, tea.WindowSize()
	}

	// Submit: resolve the alias to a Host.
	alias := m.hostSelector.SelectedAlias()
	if alias == "" {
		m.hostSelector.Submitted = false
		return m, m.handleError(fmt.Errorf("please select a host or type an alias"))
	}

	// If the alias was typed freely (not picked from the registry and not
	// local), register it so it reappears next time. Best-effort.
	if m.hostSelector.IsFreeAlias() && alias != "local" && m.hostRegistry != nil {
		_ = m.hostRegistry.Add(alias)
	}
	// MRU: move the selected alias to the head of the registry so it is
	// offered at the top next time. Best-effort; local is a no-op.
	if alias != "local" && m.hostRegistry != nil {
		_ = m.hostRegistry.Touch(alias)
	}

	promptFlow := m.repoSelectPrompt
	m.pendingHost = host.Lookup(alias)
	m.hostSelector = nil
	// Proceed to the repo selector, which will validate the repo path using
	// the chosen host's executor (so a remote repo path is checked remotely).
	return m, m.openRepoSelector(promptFlow)
}

// applyHostDeletions persists any hosts removed via ctrl+d in the host
// selector. Silent and best-effort: a failure to write does not block the
// selection flow. "local" is never deletable (protected in ListSelector).
func (m *home) applyHostDeletions() {
	if m.hostSelector == nil || m.hostRegistry == nil {
		return
	}
	for _, alias := range m.hostSelector.TakeDeletedValues() {
		_ = m.hostRegistry.Remove(alias)
	}
}
