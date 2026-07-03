package git

import (
	"os/exec"
	"testing"

	cmdtest "claude-squad/cmd/cmd_test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGitWorktree_RunGitCommand_RoutesViaExecutor proves the seam:
// GitWorktree.runGitCommand builds a `git -C <path> <args...>` command and
// routes it through the injected Executor, not exec.* directly. This is the
// guarantee that v2 can swap the Executor for an SSH transport without
// touching GitWorktree.
func TestGitWorktree_RunGitCommand_RoutesViaExecutor(t *testing.T) {
	var got *exec.Cmd
	executor := cmdtest.MockCmdExec{
		CombinedOutputFunc: func(c *exec.Cmd) ([]byte, error) {
			got = c
			return []byte("ok\n"), nil
		},
	}

	// Construct directly (in-package test) with the mock executor.
	g := &GitWorktree{
		repoPath:     "/repo",
		worktreePath: "/repo/.cs2/worktrees/wt",
		branchName:   "cs2/feat",
		cmdExec:      executor,
	}

	out, err := g.runGitCommand("/repo/.cs2/worktrees/wt", "status", "--porcelain")
	require.NoError(t, err)
	assert.Equal(t, "ok\n", out)

	// Command routed through the executor (not exec.* directly).
	require.NotNil(t, got, "command must be routed via the executor")
	assert.Equal(t, "git", got.Args[0], "first arg is the git binary name")

	// Args: git -C <path> <args...>. Proves the path is passed via -C and the
	// extra args are appended verbatim — so an SSH transport that wraps
	// `git -C path ...` as `ssh host git -C path ...` works unchanged.
	assert.Equal(t,
		[]string{"git", "-C", "/repo/.cs2/worktrees/wt", "status", "--porcelain"},
		got.Args)
}

// TestGitWorktree_NewGitWorktreeWithDeps_DefaultsExecutor proves the public
// WithDeps constructor wires the injected executor into the struct, so v2 can
// build a worktree bound to an SSH executor.
func TestGitWorktree_NewGitWorktreeWithDeps_WiresExecutor(t *testing.T) {
	// Use a real temp git repo so resolveWorktreePaths (which calls Root via
	// the executor) succeeds.
	repoPath := t.TempDir()
	mustRunGit(t, "", "init", repoPath)
	mustRunGit(t, repoPath, "config", "user.name", "Test")
	mustRunGit(t, repoPath, "config", "user.email", "t@e.com")
	mustRunGit(t, repoPath, "commit", "--allow-empty", "-m", "init")

	var seenCmd *exec.Cmd
	executor := cmdtest.MockCmdExec{
		OutputFunc: func(c *exec.Cmd) ([]byte, error) {
			seenCmd = c
			// Pretend the repo root is repoPath itself.
			return []byte(repoPath + "\n"), nil
		},
		RunFunc: func(c *exec.Cmd) error { return nil },
	}

	g, _, err := NewGitWorktreeWithDeps(repoPath, "sess", executor)
	require.NoError(t, err)
	require.NotNil(t, g)

	// Root() was invoked via the injected executor during path resolution.
	require.NotNil(t, seenCmd, "Repo.Root must route through the injected executor")
	assert.Equal(t, []string{"git", "-C", repoPath, "rev-parse", "--show-toplevel"}, seenCmd.Args)
}
