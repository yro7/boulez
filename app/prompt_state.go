package app

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/ui"
)

// handlePromptState dispatches key events while the prompt+branch overlay is
// open (statePrompt). Extracted from handleKeyPress so the overlay's lifecycle
// (cancel, submit, branch-search debounce, profile-preference save, and the
// Shift+N spawn-vs-running SendPrompt fork) lives in one place. The earlier
// "see handlePromptState" comment in the home struct referred to this method
// before the extraction had happened; it now exists.
func (m *home) handlePromptState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Handle cancel via ctrl+c before delegating to the overlay
	if msg.String() == "ctrl+c" {
		return m, m.cancelPromptOverlay()
	}

	// ctrl+s on the profile picker records an explicit repo→profile
	// preference for the selected instance's repo, so the prompt overlay
	// preselects this profile next time. Best-effort: a nil prefs store
	// or a failure to persist never blocks the prompt flow.
	if msg.String() == "ctrl+s" {
		return m, m.saveProfilePreference()
	}

	// Use the new TextInputOverlay component to handle all key events
	shouldClose, branchFilterChanged := m.textInputOverlay.HandleKeyPress(msg)

	// Check if the form was submitted or canceled
	if shouldClose {
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}

		if m.textInputOverlay.IsCanceled() {
			return m, m.cancelPromptOverlay()
		}

		if m.textInputOverlay.IsSubmitted() {
			prompt := m.textInputOverlay.GetValue()
			selectedBranch := m.textInputOverlay.GetSelectedBranch()
			selectedProgram := m.textInputOverlay.GetSelectedProgram()

			if !selected.Started() {
				// Shift+N flow: instance not started yet — hand the prompt+
				// branch off to the kernel spawn syscall (C3.3). The draft
				// stays Loading while the kernel creates the real instance.
				if selectedBranch != "" {
					selected.SetSelectedBranch(selectedBranch)
				}
				if selectedProgram != "" {
					selected.Program = selectedProgram
				}
				selected.Prompt = prompt

				// Finalize into list and spawn via the kernel.
				selected.SetStatus(session.Loading)
				m.newInstanceFinalizer()
				m.textInputOverlay = nil
				m.state = stateDefault
				m.menu.SetState(ui.StateDefault)

				opts := SpawnOptions{
					Repo:    selected.Path,
					Title:   selected.Title,
					Program: selected.Program,
					Branch:  selected.SelectedBranch(),
					Prompt:  prompt,
				}
				return m, tea.Batch(tea.WindowSize(), m.instanceChanged(), m.runSpawnCmd(opts, selected.GetID(), false))
			}

			// Regular flow: instance already running, just send prompt
			if err := selected.SendPrompt(prompt); err != nil {
				return m, m.handleError(err)
			}
		}

		// Close the overlay and reset state
		m.textInputOverlay = nil
		m.state = stateDefault
		return m, tea.Sequence(
			tea.WindowSize(),
			func() tea.Msg {
				m.menu.SetState(ui.StateDefault)
				m.showHelpScreen(helpStart(selected), nil)
				return nil
			},
		)
	}

	// Schedule a debounced branch search if the filter changed
	if branchFilterChanged {
		filter := m.textInputOverlay.BranchFilter()
		version := m.textInputOverlay.BranchFilterVersion()
		repoPath := ""
		if selected := m.list.GetSelectedInstance(); selected != nil {
			repoPath = selected.Path
		}
		return m, m.scheduleBranchSearch(repoPath, filter, version)
	}

	return m, nil
}
