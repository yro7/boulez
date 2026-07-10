package session

import (
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yro7/boulez/cmd"
	"github.com/yro7/boulez/cmd/cmd_test"
	"github.com/yro7/boulez/session/git"
	"github.com/yro7/boulez/session/tmux"
)

// archiveFakeWorktree is a minimal Worktree double for the Archive test. Its
// only contract: Cleanup must NOT be called by Archive (the soft delete keeps
// the worktree + branch), so it panics to surface any unintended coupling.
type archiveFakeWorktree struct{}

func (archiveFakeWorktree) Setup() error                      { panic("unexpected") }
func (archiveFakeWorktree) Cleanup() error                    { panic("Cleanup must not run on Archive") }
func (archiveFakeWorktree) Remove() error                     { panic("unexpected") }
func (archiveFakeWorktree) Prune() error                      { panic("unexpected") }
func (archiveFakeWorktree) IsValidWorktree() (bool, error)    { return true, nil }
func (archiveFakeWorktree) WorktreeDirExists() bool           { return true }
func (archiveFakeWorktree) IsBranchCheckedOut() (bool, error) { return false, nil }
func (archiveFakeWorktree) IsExistingBranch() bool            { return false }
func (archiveFakeWorktree) RemoveWorktreeDir() error          { panic("unexpected") }
func (archiveFakeWorktree) IsDirty() (bool, error)            { return false, nil }
func (archiveFakeWorktree) CommitChanges(string) error        { panic("unexpected") }
func (archiveFakeWorktree) PushChanges(string, bool) error     { panic("unexpected") }
func (archiveFakeWorktree) Diff() *git.DiffStats               { return nil }
func (archiveFakeWorktree) DiffNumstat() *git.DiffStats       { return nil }
func (archiveFakeWorktree) GetRepoPath() string               { return "/repo" }
func (archiveFakeWorktree) GetWorktreePath() string            { return "/wt" }
func (archiveFakeWorktree) GetBranchName() string              { return "feat" }
func (archiveFakeWorktree) GetRepoName() string                { return "repo" }
func (archiveFakeWorktree) GetBaseCommitSHA() string           { return "" }

// TestInstance_Archive_KillsTmuxKeepsWorktree proves the soft-delete unit:
// Archive kills the tmux session (kill-session is invoked) but does NOT touch
// the worktree (Cleanup would panic). This is the behavioural split from Kill,
// which destroys both.
func TestInstance_Archive_KillsTmuxKeepsWorktree(t *testing.T) {
	var kills int32
	mockExec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			if strings.Contains(cmd.ToString(c), "kill-session") {
				atomic.AddInt32(&kills, 1)
			}
			return nil
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return []byte{}, nil },
	}
	inst, err := NewInstance(InstanceOptions{Title: "soft", Path: "/repo", Kind: KindWorker})
	require.NoError(t, err)
	inst.gitWorktree = archiveFakeWorktree{}
	inst.started = true
	inst.SetTmuxSession(tmux.NewTmuxSessionWithDeps("soft", "bash", mockExec))

	require.NoError(t, inst.Archive())

	assert.Equal(t, int32(1), atomic.LoadInt32(&kills), "kill-session invoked once")
	assert.Equal(t, Archived, inst.Status, "status is Archived")
}

// TestFromInstanceData_ArchivedBindsTmuxHandle is the regression test for the
// daemon-restart half of the restore bug. After a daemon restart, an Archived
// instance is rebuilt via FromInstanceData. Previously the Archived branch set
// started=true but left tmuxSession nil, so Restore hit "cannot restore: no
// tmux session handle" — an archived instance became permanently un-restorable
// across a restart. The Archived branch must bind a fresh tmuxSession handle
// (like the Paused branch) so Restore can recreate the session on demand.
// Asserting the unexported field is honest here: the contract is precisely "a
// handle is bound", observed in-package to avoid shelling out to tmux.
func TestFromInstanceData_ArchivedBindsTmuxHandle(t *testing.T) {
	data := InstanceData{
		ID:     "arch-1",
		Title:  "soft",
		Status: Archived,
		Kind:   KindWorker,
		Worktree: GitWorktreeData{
			RepoPath:     "/repo",
			WorktreePath: "/wt",
			SessionName:  "soft-aaaaaaaa",
			BranchName:   "feat",
		},
		Program:    "bash",
		ArchivedAt: time.Now().Add(-1 * time.Hour),
	}
	inst, err := FromInstanceData(data)
	require.NoError(t, err)
	assert.Equal(t, Archived, inst.Status, "status preserved")
	assert.True(t, inst.Started(), "started so Restore knows the worktree is set up")
	require.NotNil(t, inst.tmuxSession, "Archived instance must have a tmux handle so Restore works after a daemon restart")
}

// TestInstance_Restore_RequiresArchived proves Restore's status guard: only
// an Archived instance can be restored. Documents the invariant the kernel's
// UpdateStatus guard relies on — Archived is exited only via Restore (which
// sets Ready directly), never via the steady-state status path.
func TestInstance_Restore_RequiresArchived(t *testing.T) {
	mockExec := cmd_test.MockCmdExec{
		RunFunc:    func(c *exec.Cmd) error { return nil },
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return []byte{}, nil },
	}
	inst, err := NewInstance(InstanceOptions{Title: "soft", Path: "/repo", Kind: KindWorker})
	require.NoError(t, err)
	inst.gitWorktree = archiveFakeWorktree{}
	inst.started = true
	inst.SetTmuxSession(tmux.NewTmuxSessionWithDeps("soft", "bash", mockExec))
	inst.SetStatus(Running) // not Archived

	err = inst.Restore()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "can only restore archived instances")
}
