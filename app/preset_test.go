package app

import (
	"claude-squad/config"
	"claude-squad/host"
	"claude-squad/presets"
	"claude-squad/ui"
	"context"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPresetHome builds a home wired with a temp-backed preset store and a
// multi-profile config, enough to drive the preset selector flow.
func newPresetHome(t *testing.T) *home {
	t.Helper()
	h := newRepoSelectHome(t)
	h.presetStore = presets.NewStoreAt(filepath.Join(t.TempDir(), "presets.json"))
	h.appConfig = profilesForPrefs(t)
	h.menu = ui.NewMenu()
	h.ctx = context.Background()
	return h
}

// TestPresetSelectorEmptyStoreShowsError proves that Ctrl+R with no presets
// defined surfaces an error pointing at the file rather than an empty picker.
func TestPresetSelectorEmptyStoreShowsError(t *testing.T) {
	h := newPresetHome(t)

	cmd := h.openPresetSelector()
	if cmd != nil {
		_ = cmd()
	}

	assert.NotEqual(t, statePresetSelect, h.state, "must not open the picker with no presets")
	assert.Nil(t, h.presetSelector)
	// An error was surfaced to the err box.
	assert.True(t, h.errBox.HasError())
}

// TestPresetSelectorOpensWithPresets proves Ctrl+R opens the picker populated
// with the stored preset names.
func TestPresetSelectorOpensWithPresets(t *testing.T) {
	h := newPresetHome(t)
	repo := t.TempDir()
	makeTestRepo(t, repo)
	require.NoError(t, h.presetStore.Set("CS2 Work", presets.Preset{Repo: repo, Host: "local", Profile: "Claude"}))

	h.openPresetSelector()

	require.NotNil(t, h.presetSelector)
	assert.Equal(t, statePresetSelect, h.state)
	// The filter is empty so all presets are visible.
	assert.Equal(t, 1, h.presetSelector.NumRows())
}

// TestPresetSelectorSubmitStartsNameEntry proves selecting a preset jumps
// straight to name entry (stateNew) with the host/repo/profile/branch applied
// to the instance, skipping the host/repo/prompt selectors entirely.
func TestPresetSelectorSubmitStartsNameEntry(t *testing.T) {
	h := newPresetHome(t)
	repo := t.TempDir()
	makeTestRepo(t, repo)
	require.NoError(t, h.presetStore.Set("CS2 Work", presets.Preset{
		Repo:    repo,
		Host:    "local",
		Profile: "Pi",
	}))

	h.openPresetSelector()

	// Submit on the first (only) preset.
	h.handlePresetSelectState(tea.KeyMsg{Type: tea.KeyEnter})

	assert.Equal(t, stateNew, h.state, "must jump straight to name entry")
	assert.Nil(t, h.presetSelector, "selector must be torn down after submit")
	assert.False(t, h.promptAfterName, "empty prompt must not open the prompt overlay")

	inst := h.list.GetInstances()[h.list.NumInstances()-1]
	assert.Equal(t, repo, inst.Path)
	assert.Equal(t, "pi", inst.Program, "profile name must be resolved to the program string")
	_, isLocal := inst.Host().(host.LocalHost)
	assert.True(t, isLocal, "local preset must resolve to LocalHost")
	assert.Empty(t, inst.Prompt, "empty preset prompt must not stash a prompt")
}

// TestPresetSelectorStashesPromptForAutoSend proves a preset with a prompt
// stashes it on the instance (auto-sent after Start) rather than opening the
// prompt overlay — the quick-session contract.
func TestPresetSelectorStashesPromptForAutoSend(t *testing.T) {
	h := newPresetHome(t)
	repo := t.TempDir()
	makeTestRepo(t, repo)
	require.NoError(t, h.presetStore.Set("Work", presets.Preset{
		Repo:   repo,
		Host:   "local",
		Prompt: "fix the bug",
	}))

	h.openPresetSelector()
	h.handlePresetSelectState(tea.KeyMsg{Type: tea.KeyEnter})

	assert.Equal(t, stateNew, h.state)
	assert.False(t, h.promptAfterName, "prompt must be auto-sent, not opened in an overlay")
	inst := h.list.GetInstances()[h.list.NumInstances()-1]
	assert.Equal(t, "fix the bug", inst.Prompt, "prompt must be stashed for the start handler to send")
}

// TestPresetSelectorRejectsInvalidRepo proves a preset pointing at a non-git
// directory is rejected with an error, never creating an instance.
func TestPresetSelectorRejectsInvalidRepo(t *testing.T) {
	h := newPresetHome(t)
	// A temp dir that is NOT a git repo.
	notRepo := t.TempDir()
	require.NoError(t, h.presetStore.Set("Bad", presets.Preset{Repo: notRepo, Host: "local"}))

	h.openPresetSelector()
	h.handlePresetSelectState(tea.KeyMsg{Type: tea.KeyEnter})

	assert.Equal(t, stateDefault, h.state, "invalid repo must abort back to default")
	assert.Equal(t, 0, h.list.NumInstances(), "no instance must be created")
	assert.True(t, h.errBox.HasError())
}

// TestPresetSelectorRejectsUnknownProfile proves a preset referencing a profile
// that does not exist in the config is rejected, rather than silently falling
// back to the default program.
func TestPresetSelectorRejectsUnknownProfile(t *testing.T) {
	h := newPresetHome(t)
	repo := t.TempDir()
	makeTestRepo(t, repo)
	require.NoError(t, h.presetStore.Set("Bad", presets.Preset{Repo: repo, Host: "local", Profile: "Ghost"}))

	h.openPresetSelector()
	h.handlePresetSelectState(tea.KeyMsg{Type: tea.KeyEnter})

	assert.Equal(t, stateDefault, h.state)
	assert.Equal(t, 0, h.list.NumInstances())
	assert.True(t, h.errBox.HasError())
}

// TestPresetSelectorCancelReturnsToDefault proves Esc/ctrl+c closes the picker
// without creating an instance.
func TestPresetSelectorCancelReturnsToDefault(t *testing.T) {
	h := newPresetHome(t)
	repo := t.TempDir()
	makeTestRepo(t, repo)
	require.NoError(t, h.presetStore.Set("Work", presets.Preset{Repo: repo, Host: "local"}))

	h.openPresetSelector()
	h.handlePresetSelectState(tea.KeyMsg{Type: tea.KeyEsc})

	assert.Equal(t, stateDefault, h.state)
	assert.Nil(t, h.presetSelector)
	assert.Equal(t, 0, h.list.NumInstances())
}

// TestPresetSelectorAppliesBranch proves a preset with an existing branch
// sets it as the instance's selected branch.
func TestPresetSelectorAppliesBranch(t *testing.T) {
	h := newPresetHome(t)
	repo := t.TempDir()
	makeTestRepo(t, repo) // creates feature-one, feature-two
	require.NoError(t, h.presetStore.Set("Work", presets.Preset{
		Repo:   repo,
		Host:   "local",
		Branch: "feature-one",
	}))

	h.openPresetSelector()
	h.handlePresetSelectState(tea.KeyMsg{Type: tea.KeyEnter})

	assert.Equal(t, stateNew, h.state)
	// The selected branch is private; we verify indirectly by checking the
	// flow reached name entry without error (the branch exists).
	assert.False(t, h.errBox.HasError())
}

// TestGetProfileByName resolves a known profile name to its program and reports
// false for an unknown name.
func TestGetProfileByName(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Profiles = []config.Profile{
		{Name: "Claude", Program: "claude"},
		{Name: "Pi", Program: "pi"},
	}

	prog, ok := cfg.GetProfileByName("Pi")
	require.True(t, ok)
	assert.Equal(t, "pi", prog)

	_, ok = cfg.GetProfileByName("Ghost")
	assert.False(t, ok)
}
