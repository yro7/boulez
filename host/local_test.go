package host

import (
	"os"
	"path/filepath"
	"testing"

	"claude-squad/cmd"
	"claude-squad/session/fs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLocalHost_WorktreeDir_MatchesOldPath verifies that LocalHost.WorktreeDir
// returns the same path the git layer used to compute on its own
// (getWorktreeDirectory: ~/.cs2/worktrees). This is the non-regression guard
// for Step 1: the Host abstraction must reproduce today's local behaviour
// exactly so that wiring Instance through Host (Step 1b) is behaviour-neutral.
func TestLocalHost_WorktreeDir_MatchesOldPath(t *testing.T) {
	// Isolate HOME so config.GetConfigDir is deterministic.
	tempHome := t.TempDir()
	orig := os.Getenv("HOME")
	require.NoError(t, os.Setenv("HOME", tempHome))
	defer func() { _ = os.Setenv("HOME", orig) }()

	dir, err := LocalHost{}.WorktreeDir()
	require.NoError(t, err)

	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".cs2", "worktrees")
	assert.Equal(t, want, dir)
}

// TestLocalHost_Deps returns the local implementations — proving LocalHost is
// a complete Host whose deps are the same types the rest of the codebase uses
// today (non-regression: no behaviour change from wiring through Host).
func TestLocalHost_Deps(t *testing.T) {
	h := LocalHost{}
	assert.Equal(t, "local", h.Name())

	// Executor is the local cmd executor (non-nil, usable).
	exec := h.Executor()
	_, ok := exec.(cmd.Exec)
	assert.True(t, ok, "LocalHost.Executor should be cmd.Exec")

	// FS is the local filesystem.
	_, ok = h.FS().(fs.LocalFS)
	assert.True(t, ok, "LocalHost.FS should be fs.LocalFS")

	// PtyFactory is the local pty factory (non-nil).
	assert.NotNil(t, h.PtyFactory())
}

// TestLocalHost_ResolveRepoPath_Absolutizes proves the local branch of
// transport-specific path resolution: LocalHost resolves a relative path
// against the process cwd (filepath.Abs). A stored relative path thus survives
// a cwd change. Absolute paths are returned unchanged. This is the contract
// buildWorktree relies on for LocalHost — and the counterpart to SSHHost's
// passthrough (TestSSHHost_ResolveRepoPath_Passthrough).
func TestLocalHost_ResolveRepoPath_Absolutizes(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)

	// Relative -> resolved against the cwd.
	got := LocalHost{}.ResolveRepoPath("foo/bar")
	assert.Equal(t, filepath.Join(wd, "foo/bar"), got)

	// Absolute -> returned unchanged (cleaned).
	abs := filepath.Join(wd, "repo")
	assert.Equal(t, abs, LocalHost{}.ResolveRepoPath(abs))

	// ~ is NOT expanded by LocalHost (that is the remote shell's job, not
	// filepath.Abs's) — pinning so a future "helpful" ~ expansion here fails.
	assert.Equal(t, filepath.Join(wd, "~/repo"), LocalHost{}.ResolveRepoPath("~/repo"))
}
