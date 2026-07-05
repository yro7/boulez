package app

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yro7/boulez/host"
	"github.com/yro7/boulez/session/git"
	"github.com/yro7/boulez/ui/overlay"
)

// This file groups the repo-selector overlay lifecycle: opening it (after a
// host is chosen), filtering registered repos against the host's executor,
// dispatching key presses while open, validating the chosen path on submit,
// and persisting ctrl+d removals to the repo registry. Extracted from app.go.
// Pure move: same receiver, same signatures, same behavior.

// filterRepos returns a tea.Cmd that probes each registered repo against the
// chosen host's executor and returns only those that exist on that host. Local
// is instant; remote fans out concurrently (one `ssh host git -C <path> ...`
// per repo). The repo selector starts with the full list and is narrowed when
// this result lands, so local users see no flicker while remote users see the
// inaccessible entries drop once probed.
func (m *home) filterRepos(repos []string, h host.Host) tea.Cmd {
	return func() tea.Msg {
		return reposFilteredMsg{repos: git.FilterExistingRepos(repos, h.Executor())}
	}
}

// openRepoSelector opens the repo selector overlay before creating a new
// instance. promptFlow controls whether the prompt+branch overlay follows name
// entry (KeyPrompt) or not (KeyNew). The repo path is validated against
// m.pendingHost's executor (local or ssh).
func (m *home) openRepoSelector(promptFlow bool) tea.Cmd {
	var repos []string
	if m.repoRegistry != nil {
		repos, _ = m.repoRegistry.List()
	}
	m.repoSelector = overlay.NewRepoSelector(repos)
	m.repoSelectPrompt = promptFlow
	m.state = stateRepoSelect

	// Filter the registered repos against the chosen host's executor: a remote
	// host can only run instances from repos that exist on that machine, so a
	// local-only repo is hidden rather than offered (and rejected at submit).
	// Local is instant; remote fans out one `ssh host git -C <path> ...` per
	// repo concurrently. The selector starts with the full list and is
	// narrowed when the result lands — local users see no flicker, remote
	// users see the inaccessible entries drop once probed.
	h := m.pendingHost
	if h == nil {
		h = host.Local
	}
	return tea.Batch(tea.WindowSize(), m.filterRepos(repos, h))
}

// handleRepoSelectState dispatches key presses to the repo selector overlay
// and finalizes the selection on submit/cancel.
func (m *home) handleRepoSelectState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.repoSelector == nil {
		m.state = stateDefault
		return m, nil
	}

	// ctrl+c cancels like esc.
	if msg.String() == "ctrl+c" {
		m.repoSelector.Canceled = true
	} else {
		shouldClose := m.repoSelector.HandleKeyPress(msg)
		// Apply silent ctrl+d removals to the persistent repo registry. Done
		// on every non-close keypress (TakeDeletedValues is a no-op when
		// nothing was deleted).
		m.applyRepoDeletions()
		if !shouldClose {
			return m, nil
		}
	}

	if m.repoSelector.Canceled {
		m.repoSelector = nil
		m.state = stateDefault
		return m, tea.WindowSize()
	}

	// Submit: validate the chosen path against the selected host's executor
	// (so a remote repo path is checked on the right machine).
	h := m.pendingHost
	if h == nil {
		h = host.Local
	}
	selected := m.repoSelector.SelectedPath()
	if selected == "" {
		m.repoSelector.Submitted = false
		return m, m.handleError(fmt.Errorf("please select a repo or type a path"))
	}
	if !git.NewRepoWithDeps(selected, h.Executor()).IsGitRepo() {
		m.repoSelector.Submitted = false
		return m, m.handleError(fmt.Errorf("not a git repository: %s", selected))
	}

	// If the path was typed freely (not picked from the registry), register it
	// so it reappears next time. Best-effort: a failure here does not block
	// instance creation.
	if m.repoSelector.IsFreePath() && m.repoRegistry != nil {
		_ = m.repoRegistry.Add(selected)
	}
	// MRU: move the selected repo to the head of the registry so it is
	// offered at the top next time. Best-effort.
	if m.repoRegistry != nil {
		_ = m.repoRegistry.Touch(selected)
	}

	promptFlow := m.repoSelectPrompt
	repoPath := selected
	m.repoSelector = nil
	return m, m.startNewInstance(repoPath, promptFlow)
}

// applyRepoDeletions persists any repos removed via ctrl+d in the repo
// selector. Silent and best-effort.
func (m *home) applyRepoDeletions() {
	if m.repoSelector == nil || m.repoRegistry == nil {
		return
	}
	for _, path := range m.repoSelector.TakeDeletedValues() {
		_ = m.repoRegistry.Remove(path)
	}
}
