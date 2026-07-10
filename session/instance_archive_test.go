package session

import (
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

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
