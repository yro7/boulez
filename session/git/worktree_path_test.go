package git

import (
	"os/exec"
	"path/filepath"
	"testing"

	cmdtest "claude-squad/cmd/cmd_test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveWorktreePaths_RelativePathPassedAsIs is the regression guard for
// the remote-path bug: resolveWorktreePaths must NOT call filepath.Abs on the
// repo path. Doing so resolves a relative path against the LOCAL cwd — which
// corrupts a remote relative path (e.g. "testgit" -> "/Users/local/.../testgit",
// a path on the wrong machine) and was the root cause of the
// "not a git repository" error on a remote host.
//
// Path resolution is now transport-specific and lives on Host.ResolveRepoPath,
// called by the instance before reaching the git layer. So resolveWorktreePaths
// must pass the path verbatim to `git -C <path>` (git resolves it; Root()
// returns git's absolute toplevel regardless). We assert the path reaching the
// executor is the input unchanged — not absolutized.
func TestResolveWorktreePaths_RelativePathPassedAsIs(t *testing.T) {
	const rel = "testgit"

	var seenCmd *exec.Cmd
	executor := cmdtest.MockCmdExec{
		OutputFunc: func(c *exec.Cmd) ([]byte, error) {
			seenCmd = c
			// Pretend git's toplevel is an absolute path (as git would return).
			return []byte("/home/freebox/testgit\n"), nil
		},
	}

	resolvedRepo, wtPath, err := resolveWorktreePaths(rel, "sess", executor, filepath.Join(t.TempDir(), "worktrees"))
	require.NoError(t, err)

	// The path reaching `git -C` is the relative input verbatim — NOT
	// absolutized against the local cwd. This is the property the bug broke.
	require.NotNil(t, seenCmd, "Repo.Root must be called via the executor")
	assert.Equal(t, []string{"git", "-C", rel, "rev-parse", "--show-toplevel"}, seenCmd.Args,
		"resolveWorktreePaths must pass the path to git -C verbatim, never absolutize it locally")

	// resolvedRepo is whatever git's Root() returned (absolute, from git — not
	// from a local filepath.Abs). wtPath is under the given worktreeDir.
	assert.Equal(t, "/home/freebox/testgit", resolvedRepo)
	assert.Contains(t, wtPath, "worktrees", "worktree path is under the given worktreeDir")
}

// TestResolveWorktreePaths_TildePathPassedAsIs pins that a ~-relative path
// (the SSHHost worktree dir is "~/.cs2/worktrees") reaches git -C unmodified —
// filepath.Abs would resolve ~ against the local $HOME, pointing at the wrong
// machine. The remote shell must expand ~, not the local process.
func TestResolveWorktreePaths_TildePathPassedAsIs(t *testing.T) {
	const tildePath = "~/repos/proj"

	var seenCmd *exec.Cmd
	executor := cmdtest.MockCmdExec{
		OutputFunc: func(c *exec.Cmd) ([]byte, error) {
			seenCmd = c
			return []byte("/home/freebox/repos/proj\n"), nil
		},
	}

	_, _, err := resolveWorktreePaths(tildePath, "sess", executor, filepath.Join(t.TempDir(), "worktrees"))
	require.NoError(t, err)
	require.NotNil(t, seenCmd)
	assert.Equal(t, []string{"git", "-C", tildePath, "rev-parse", "--show-toplevel"}, seenCmd.Args,
		"~-relative path must reach git -C verbatim for the remote shell to expand")
}
