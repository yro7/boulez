package git

import (
	"os/exec"
	"strings"
	"testing"

	cmdtest "claude-squad/cmd/cmd_test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRepo_SearchBranches_RoutesViaExecutor proves the seam: Repo.SearchBranches
// builds a `git -C <path> branch -a ...` command and routes it through the
// injected Executor, rather than calling exec directly. This is the guarantee
// that v2 can swap the Executor for an SSH transport without touching Repo.
func TestRepo_SearchBranches_RoutesViaExecutor(t *testing.T) {
	var got *exec.Cmd
	executor := cmdtest.MockCmdExec{
		CombinedOutputFunc: func(c *exec.Cmd) ([]byte, error) {
			got = c
			// Minimal plausible `git branch -a --format=...` output.
			return []byte("main\norigin/main\nfeature/x\n"), nil
		},
	}

	r := NewRepoWithDeps("/some/repo/path", executor)
	branches, err := r.SearchBranches("feat")
	require.NoError(t, err)

	// The command is routed through the executor, not exec.* directly.
	require.NotNil(t, got, "command must be routed via the executor")
	assert.Equal(t, "git", got.Args[0], "first arg is the git binary name")

	// Args: -C <path> branch -a --sort=-committerdate --format=...
	// Proves the path is passed via -C (not cwd / .Dir) and the branch-listing
	// flags are intact — so an SSH transport that wraps `git -C path ...` as
	// `ssh host git -C path ...` works unchanged.
	wantArgs := []string{"git", "-C", "/some/repo/path", "branch", "-a",
		"--sort=-committerdate", "--format=%(refname:short)"}
	assert.Equal(t, wantArgs, got.Args)

	// Dedup: origin/main collapses onto main; filter keeps only feature/x.
	assert.Equal(t, []string{"feature/x"}, branches)
}

// TestRepo_FetchBranches_RoutesViaExecutor proves the fetch path goes through
// the executor with the right args (so v2's SSH executor sees it).
func TestRepo_FetchBranches_RoutesViaExecutor(t *testing.T) {
	var got *exec.Cmd
	executor := cmdtest.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			got = c
			return nil
		},
	}

	r := NewRepoWithDeps("/repo", executor)
	r.FetchBranches() // best-effort, no return value

	require.NotNil(t, got)
	assert.Equal(t, []string{"git", "-C", "/repo", "fetch", "--prune"}, got.Args)
}

// TestRepo_Root_RoutesViaExecutor proves Root() routes `rev-parse
// --show-toplevel` through the executor (was findGitRepoRoot, which called
// exec directly).
func TestRepo_Root_RoutesViaExecutor(t *testing.T) {
	var got *exec.Cmd
	executor := cmdtest.MockCmdExec{
		OutputFunc: func(c *exec.Cmd) ([]byte, error) {
			got = c
			return []byte("/resolved/top/level\n"), nil
		},
	}

	r := NewRepoWithDeps("/some/path", executor)
	root, err := r.Root()
	require.NoError(t, err)

	require.NotNil(t, got)
	assert.Equal(t, []string{"git", "-C", "/some/path", "rev-parse", "--show-toplevel"}, got.Args)
	assert.Equal(t, "/resolved/top/level", root)
}

// TestRepo_IsGitRepo_RoutesViaExecutor proves IsGitRepo() routes through the
// executor (was a package-level func calling exec directly). The Run result
// drives the bool return.
func TestRepo_IsGitRepo_RoutesViaExecutor(t *testing.T) {
	var calls int
	executor := cmdtest.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			calls++
			if strings.Contains(strings.Join(c.Args, " "), "rev-parse --show-toplevel") {
				return nil // success → IsGitRepo true
			}
			return assert.AnError
		},
	}

	r := NewRepoWithDeps("/repo", executor)
	assert.True(t, r.IsGitRepo())
	assert.Equal(t, 1, calls, "IsGitRepo should issue exactly one Run via the executor")
}
