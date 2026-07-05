package app

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yro7/boulez/host"
	"github.com/yro7/boulez/presets"
	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/session/git"
	"github.com/yro7/boulez/ui"
	"github.com/yro7/boulez/ui/overlay"
)

// This file groups the named-preset selector lifecycle (Ctrl+R): opening the
// preset picker, dispatching key presses while open, and on submit applying
// the preset's host/repo/branch/prompt and jumping straight to name entry
// (skipping the host/repo/prompt overlays). Extracted from app.go. Pure move:
// same receiver, same signatures, same behavior.

// openPresetSelector opens the named-preset picker (Ctrl+R). Presets are read
// fresh from ~/.boulez/presets.json on every open, so an agent or editor can
// change the file between two opens with no watcher. An empty store is shown
// as an error pointing at the file rather than an empty picker.
func (m *home) openPresetSelector() tea.Cmd {
	var names []string
	if m.presetStore != nil {
		names, _ = m.presetStore.List()
	}
	if len(names) == 0 {
		path := "~/.boulez/presets.json"
		if m.presetStore != nil {
			path = m.presetStore.Path()
		}
		return m.handleError(fmt.Errorf("no presets defined — add one to %s", path))
	}
	m.presetSelector = overlay.NewPresetSelector(names)
	m.state = statePresetSelect
	return tea.WindowSize()
}

// handlePresetSelectState dispatches key presses to the preset selector overlay
// and finalizes the selection on submit/cancel. On submit it resolves the
// preset to a host + repo + profile + branch + prompt, validates them against
// the registries/config, and jumps straight to name entry (stateNew), skipping
// the host/repo/prompt overlays.
func (m *home) handlePresetSelectState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.presetSelector == nil {
		m.state = stateDefault
		return m, nil
	}

	if msg.String() == "ctrl+c" {
		m.presetSelector.Canceled = true
	} else {
		shouldClose := m.presetSelector.HandleKeyPress(msg)
		if !shouldClose {
			return m, nil
		}
	}

	if m.presetSelector.Canceled {
		m.presetSelector = nil
		m.state = stateDefault
		return m, tea.WindowSize()
	}

	name := m.presetSelector.SelectedPreset()
	if name == "" {
		m.presetSelector.Submitted = false
		return m, m.handleError(fmt.Errorf("please select a preset"))
	}

	if m.presetStore == nil {
		m.presetSelector = nil
		m.state = stateDefault
		return m, m.handleError(fmt.Errorf("preset store unavailable"))
	}
	preset, ok, err := m.presetStore.Get(name)
	if err != nil || !ok {
		m.presetSelector = nil
		m.state = stateDefault
		return m, m.handleError(fmt.Errorf("preset %q not found", name))
	}

	m.presetSelector = nil
	m.state = stateDefault
	return m, m.startNewInstanceFromPreset(name, preset)
}

// startNewInstanceFromPreset applies a preset's host/repo/profile/branch and
// jumps straight to name entry (stateNew). The prompt overlay selectors are
// skipped entirely. Validation mirrors the normal flow: the repo must be a
// git repo (checked against the preset's host executor), and the profile name
// must resolve to a known config.Profile. The prompt, if any, is stashed on
// the instance and sent after Start.
func (m *home) startNewInstanceFromPreset(name string, p presets.Preset) tea.Cmd {
	repoPath := p.Repo
	if repoPath == "" {
		return m.handleError(fmt.Errorf("preset %q: repo is required", name))
	}

	// Resolve the host. "local" / empty → Local; anything else is treated as
	// an ssh alias (Lookup constructs an SSHHost regardless of registry
	// membership — a preset is an explicit recipe, not a registry mutation).
	h := host.Lookup(p.Host)

	// Validate the repo against the chosen host's executor (so a remote repo
	// is checked on the right machine), mirroring handleRepoSelectState.
	if !git.NewRepoWithDeps(repoPath, h.Executor()).IsGitRepo() {
		return m.handleError(fmt.Errorf("preset %q: not a git repository: %s", name, repoPath))
	}

	// Resolve the profile name to a program string. An empty profile means
	// "use the default program" (the boulez --program flag). A name that matches
	// no profile is rejected so a stale preset does not start a wrong agent.
	program := m.program
	if p.Profile != "" {
		resolved, ok := m.appConfig.GetProfileByName(p.Profile)
		if !ok {
			return m.handleError(fmt.Errorf("preset %q: unknown profile %q", name, p.Profile))
		}
		program = resolved
	}

	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   "",
		Path:    repoPath,
		Program: program,
	})
	if err != nil {
		return m.handleError(err)
	}
	if err := instance.SetHost(h); err != nil {
		return m.handleError(err)
	}
	if p.Branch != "" {
		instance.SetSelectedBranch(p.Branch)
	}
	if p.Prompt != "" {
		instance.Prompt = p.Prompt
	}

	m.newInstanceFinalizer = m.list.AddInstance(instance)
	m.trackDraft(instance.GetID())
	m.list.SetSelectedInstance(m.list.NumInstances() - 1)
	m.pendingHost = nil
	// A preset is a complete recipe: the host/repo/prompt selectors are
	// skipped entirely. Name entry is the only remaining step. The prompt,
	// if any, is stashed on the instance and auto-sent after Start by the
	// instanceStartedMsg handler (the same path the Shift+N flow uses), so
	// no prompt overlay is shown — that is the point of a quick session.
	if p.Prompt != "" {
		instance.Prompt = p.Prompt
	}
	m.promptAfterName = false
	m.state = stateNew
	m.menu.SetState(ui.StateNewInstance)
	return tea.WindowSize()
}
