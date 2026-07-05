package app

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/ui"
)

// handleNewState dispatches key events while the user is typing a new
// instance's title (stateNew): ctrl+c/Esc cancel, runes/Space/Backspace
// edit the title, Enter finalizes — either into the prompt overlay
// (promptAfterName flow) or straight into a kernel spawn syscall (C3.3).
// Extracted verbatim from handleKeyPress so the name-entry lifecycle lives
// in one place, mirroring handlePromptState / handleHelpState.
func (m *home) handleNewState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Handle quit commands first. Don't handle q because the user might want to type that.
	if msg.String() == "ctrl+c" {
		if draft := m.list.GetSelectedInstance(); draft != nil {
			m.untrackDraft(draft.GetID())
		}
		m.state = stateDefault
		m.promptAfterName = false
		m.list.Kill()
		return m, tea.Sequence(
			tea.WindowSize(),
			func() tea.Msg {
				m.menu.SetState(ui.StateDefault)
				return nil
			},
		)
	}

	instance := m.list.GetInstances()[m.list.NumInstances()-1]
	switch msg.Type {
	// Start the instance (enable previews etc) and go back to the main menu state.
	case tea.KeyEnter:
		if len(instance.Title) == 0 {
			return m, m.handleError(fmt.Errorf("title cannot be empty"))
		}

		// If promptAfterName, show prompt+branch overlay before starting
		if m.promptAfterName {
			m.promptAfterName = false
			m.state = statePrompt
			m.menu.SetState(ui.StatePrompt)
			m.textInputOverlay = m.newPromptOverlay(instance.Path)
			// Trigger initial branch search (no debounce, version 0) on the
			// instance's repo, not the process cwd.
			repoPath := instance.Path
			initialSearch := m.runBranchSearch(repoPath, "", m.textInputOverlay.BranchFilterVersion())
			return m, tea.Batch(tea.WindowSize(), initialSearch)
		}

		// Set Loading status and finalize into the list immediately
		instance.SetStatus(session.Loading)
		m.newInstanceFinalizer()
		m.promptAfterName = false
		m.state = stateDefault
		m.menu.SetState(ui.StateDefault)

		// Route spawn through the kernel (C3.3): the TUI keeps the draft
		// (Loading) in the list while the syscall is in flight; on ack the
		// draft is removed and the kernel's instance surfaces via the fleet
		// refresh.
		opts := SpawnOptions{
			Repo:    instance.Path,
			Title:   instance.Title,
			Program: instance.Program,
			Branch:  instance.SelectedBranch(),
		}
		if m.pendingHost != nil {
			opts.Host = m.pendingHost
			m.pendingHost = nil
		}
		return m, tea.Batch(tea.WindowSize(), m.instanceChanged(), m.runSpawnCmd(opts, instance.GetID(), false))
	case tea.KeyRunes:
		if runewidth.StringWidth(instance.Title) >= 32 {
			return m, m.handleError(fmt.Errorf("title cannot be longer than 32 characters"))
		}
		if err := instance.SetTitle(instance.Title + string(msg.Runes)); err != nil {
			return m, m.handleError(err)
		}
	case tea.KeyBackspace:
		runes := []rune(instance.Title)
		if len(runes) == 0 {
			return m, nil
		}
		if err := instance.SetTitle(string(runes[:len(runes)-1])); err != nil {
			return m, m.handleError(err)
		}
	case tea.KeySpace:
		if err := instance.SetTitle(instance.Title + " "); err != nil {
			return m, m.handleError(err)
		}
	case tea.KeyEsc:
		if draft := m.list.GetSelectedInstance(); draft != nil {
			m.untrackDraft(draft.GetID())
		}
		m.list.Kill()
		m.state = stateDefault
		m.instanceChanged()

		return m, tea.Sequence(
			tea.WindowSize(),
			func() tea.Msg {
				m.menu.SetState(ui.StateDefault)
				return nil
			},
		)
	default:
	}
	return m, nil
}
