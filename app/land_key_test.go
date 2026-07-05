package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yro7/boulez/config"
	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/session/git"
	"github.com/yro7/boulez/ui"
)

// fakeLandCaller is an app-package test double for session.LandCaller.
type fakeLandCaller struct {
	called bool
	repo   string
	target string
	source string
	result session.LandOutcome
	err    error
}

func (f *fakeLandCaller) Land(repoPath, targetBranch, sourceBranch string, strategy git.Strategy) (session.LandOutcome, error) {
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
		ctx:           context.Background(),
		state:         stateDefault,
		appConfig:     config.DefaultConfig(),
		list:          list,
		menu:          ui.NewMenu(),
		tabbedWindow:  ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane()),
		landCaller:    caller,
		landInFlight:  make(map[string]struct{}),
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

	caller := &fakeLandCaller{result: session.LandOutcome{Merge: git.MergeResult{Status: git.MergeMerged}}}
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

	caller := &fakeLandCaller{result: session.LandOutcome{Merge: git.MergeResult{Status: git.MergeMerged}}}
	h := newLandTestHome(t, inst, caller)
	h.keySent = true // bypass menu-highlight early-return so handleKeyPress reaches the switch
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("L")})
	require.Equal(t, stateConfirm, h.state)
	require.NotNil(t, h.confirmationOverlay.OnConfirm)

	// Confirm the modal — OnConfirm returns the landAction Cmd (not yet
	// executed; tea dispatches it). Invoke the Cmd to mimic the program loop
	// and prove the action runs.
	cmd := h.confirmationOverlay.OnConfirm()
	require.NotNil(t, cmd)
	_ = cmd()

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

// TestLandDoneMsg_SuccessShowsLanded proves the async land outcome reaches
// Update and surfaces a positive message + sets the Landed hint (dimmed row +
// checkmark). This is the core UX fix: before the async refactor, confirming
// the modal discarded the returned Cmd and nothing was ever shown.
func TestLandDoneMsg_SuccessShowsLanded(t *testing.T) {
	repoPath := makeTempGitRepoApp(t)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "feat-x", Path: repoPath, Program: "claude",
	})
	require.NoError(t, err)
	require.NoError(t, inst.Start(true))
	t.Cleanup(func() { _ = inst.Kill() })
	inst.SetStatus(session.Ready)

	caller := &fakeLandCaller{result: session.LandOutcome{
		Merge:      git.MergeResult{Status: git.MergeMerged},
		HostSynced: true,
	}}
	h := newLandTestHome(t, inst, caller)
	h.errBox = ui.NewErrBox()
	h.landInFlight = map[string]struct{}{inst.GetID(): {}}
	inst.SetLanding(true)

	_, _ = h.Update(landDoneMsg{
		instanceID: inst.GetID(),
		title:      inst.Title,
		target:     "main",
		result:     session.LandResult{Merge: caller.result.Merge, HostSynced: caller.result.HostSynced, HostSyncNote: caller.result.HostSyncNote},
	})

	assert.False(t, inst.Landing(), "landing hint cleared on success")
	assert.True(t, inst.Landed(), "landed hint set on success")
	_, inFlight := h.landInFlight[inst.GetID()]
	assert.False(t, inFlight, "landInFlight cleared on success")
	require.NotNil(t, h.errBox.Err())
	assert.Contains(t, h.errBox.Err().Error(), "Landed")
	assert.Contains(t, h.errBox.Err().Error(), "host synced")
}

// TestLandDoneMsg_ConflictShowsFiles proves a conflicting land surfaces the
// conflicted files and the throwaway worktree path, not a generic error.
func TestLandDoneMsg_ConflictShowsFiles(t *testing.T) {
	repoPath := makeTempGitRepoApp(t)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "feat-x", Path: repoPath, Program: "claude",
	})
	require.NoError(t, err)
	require.NoError(t, inst.Start(true))
	t.Cleanup(func() { _ = inst.Kill() })
	inst.SetStatus(session.Ready)

	caller := &fakeLandCaller{}
	h := newLandTestHome(t, inst, caller)
	h.errBox = ui.NewErrBox()
	h.landInFlight = map[string]struct{}{inst.GetID(): {}}
	inst.SetLanding(true)

	conflict := session.LandOutcome{
		Merge: git.MergeResult{
			Status:       git.MergeConflict,
			Conflicts:    []git.Conflict{{File: "a.go"}, {File: "b.go"}},
			WorktreePath: "/tmp/boulez-merge-xyz",
		},
	}
	_, _ = h.Update(landDoneMsg{
		instanceID: inst.GetID(),
		title:      inst.Title,
		target:     "main",
		result:     session.LandResult{Merge: conflict.Merge},
		err:        fmt.Errorf("merge: git merge failed"),
	})

	assert.False(t, inst.Landing(), "landing hint cleared on conflict")
	assert.False(t, inst.Landed(), "landed hint NOT set on conflict")
	require.NotNil(t, h.errBox.Err())
	assert.Contains(t, h.errBox.Err().Error(), "a.go")
	assert.Contains(t, h.errBox.Err().Error(), "b.go")
	assert.Contains(t, h.errBox.Err().Error(), "/tmp/boulez-merge-xyz")
}

// TestKeyLand_DoublePressIsNoop proves the anti-double-land guard: pressing L
// while a land is in flight for the same instance is refused with a message
// instead of a silent no-op or a second concurrent land.
func TestKeyLand_DoublePressIsNoop(t *testing.T) {
	repoPath := makeTempGitRepoApp(t)
	inst, err := session.NewInstance(session.InstanceOptions{
		Title: "feat-x", Path: repoPath, Program: "claude",
	})
	require.NoError(t, err)
	require.NoError(t, inst.Start(true))
	t.Cleanup(func() { _ = inst.Kill() })
	inst.SetStatus(session.Ready)

	caller := &fakeLandCaller{result: session.LandOutcome{Merge: git.MergeResult{Status: git.MergeMerged}}}
	h := newLandTestHome(t, inst, caller)
	h.keySent = true // bypass menu-highlight early-return so handleKeyPress reaches the switch
	h.errBox = ui.NewErrBox()
	h.landInFlight = map[string]struct{}{inst.GetID(): {}} // a land is in flight
	inst.SetLanding(true)

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("L")})

	assert.NotEqual(t, stateConfirm, h.state, "second L must not open a modal")
	assert.Nil(t, h.confirmationOverlay, "second L must not open a modal")
	assert.False(t, caller.called, "the in-flight land must not be re-invoked")
	require.NotNil(t, h.errBox.Err())
	assert.Contains(t, h.errBox.Err().Error(), "already in progress")
}
