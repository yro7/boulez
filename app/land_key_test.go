package app

import (
	"context"
	"testing"

	"claude-squad/config"
	"claude-squad/session"
	"claude-squad/session/git"
	"claude-squad/ui"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLandCaller is an app-package test double for session.LandCaller.
type fakeLandCaller struct {
	called  bool
	repo    string
	target  string
	source  string
	result  git.MergeResult
	err     error
}

func (f *fakeLandCaller) Land(repoPath, targetBranch, sourceBranch string, strategy git.Strategy) (git.MergeResult, error) {
	f.called = true
	f.repo = repoPath
	f.target = targetBranch
	f.source = sourceBranch
	return f.result, f.err
}

// newLandTestHome builds a home with a Ready instance selected and a fake
// land caller injected. The instance is constructed without Start (it has no
// real worktree), so LandInstance will hit the "no git worktree" path — to
// isolate the TUI wiring (modal + dispatch) we instead assert on the path
// the handler takes: it builds the landAction, which calls LandInstance,
// which returns an error for a headless/unstarted instance. The fake is the
// injected seam; we verify the handler routes through it when the worktree
// is present by giving the instance a started real worktree.
func newLandTestHome(t *testing.T, inst *session.Instance, caller session.LandCaller) *home {
	t.Helper()
	spin := spinner.Model{}
	list := ui.NewList(&spin, false)
	_ = list.AddInstance(inst)
	list.SetSelectedInstance(0)
	return &home{
		ctx:         context.Background(),
		state:       stateDefault,
		appConfig:   config.DefaultConfig(),
		list:        list,
		menu:        ui.NewMenu(),
		landCaller:  caller,
	}
}

// TestKeyLand_ReadyOpensModal proves pressing L on a Ready instance opens the
// confirmation modal with the target branch in the message.
func TestKeyLand_ReadyOpensModal(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "feat-x", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	inst.SetStatus(session.Ready)

	caller := &fakeLandCaller{result: git.MergeResult{Status: git.MergeMerged}}
	h := newLandTestHome(t, inst, caller)
	h.keySent = true // bypass menu-highlight early-return so handleKeyPress reaches the switch
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("L")})

	require.Equal(t, stateConfirm, h.state, "L on Ready opens the confirmation modal")
	require.NotNil(t, h.confirmationOverlay)
	rendered := h.confirmationOverlay.Render()
	assert.Contains(t, rendered, "main", "modal names the target branch")
	assert.Contains(t, rendered, "feat-x")
}

// TestKeyLand_ConfirmRunsLand proves confirming the modal invokes the injected
// LandCaller (i.e. the handler wired LandInstance to the seam).
func TestKeyLand_ConfirmRunsLand(t *testing.T) {
	// Build a started worker instance with a real git worktree so
	// LandInstance reaches the LandCaller (not the headless refusal).
	repoPath := makeTempGitRepoApp(t)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "feat-x", Path: repoPath, Program: "claude",
	})
	require.NoError(t, err)
	// Start binds the worktree. We use the real local host.
	require.NoError(t, inst.Start(true))
	t.Cleanup(func() { _ = inst.Kill() })
	inst.SetStatus(session.Ready)

	caller := &fakeLandCaller{result: git.MergeResult{Status: git.MergeMerged}}
	h := newLandTestHome(t, inst, caller)
	h.keySent = true // bypass menu-highlight early-return so handleKeyPress reaches the switch
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("L")})
	require.Equal(t, stateConfirm, h.state)
	require.NotNil(t, h.confirmationOverlay.OnConfirm)

	// Confirm the modal — this synchronously runs the landAction.
	h.confirmationOverlay.OnConfirm()

	assert.True(t, caller.called, "confirming the modal must invoke the LandCaller")
	assert.Equal(t, "main", caller.target)
	assert.NotEmpty(t, caller.source, "source branch derived from the instance")
}

// TestKeyLand_RunningIsNoop proves the guard: pressing L on a Running instance
// does nothing (no modal), so an agent mid-work is never landed.
func TestKeyLand_RunningIsNoop(t *testing.T) {
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "busy", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	inst.SetStatus(session.Running)

	caller := &fakeLandCaller{}
	h := newLandTestHome(t, inst, caller)
	h.keySent = true
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("L")})

	assert.NotEqual(t, stateConfirm, h.state, "L on Running must not open the modal")
	assert.Nil(t, h.confirmationOverlay)
	assert.False(t, caller.called)
}
