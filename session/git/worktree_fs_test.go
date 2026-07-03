package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	cmdtest "claude-squad/cmd/cmd_test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeFS is an in-memory FS recorder for seam tests. It records every path
// passed to Stat/RemoveAll/MkdirAll/ReadDir so a test can assert that the
// git layer routes through the injected FS (rather than os.* directly).
// For paths it doesn't know about, it falls back to the local filesystem so
// a real git repo in a temp dir still works.
type fakeFS struct {
	statCalls      []string
	removeAllCalls []string
	mkdirAllCalls  []string
	readDirCalls   []string

	// missingPaths are reported as non-existent (to simulate an orphaned
	// worktree). Defaults to none — everything falls through to os.*.
	missingPaths map[string]bool
}

func (f *fakeFS) Stat(name string) (os.FileInfo, error) {
	f.statCalls = append(f.statCalls, name)
	if f.missingPaths[name] {
		return nil, os.ErrNotExist
	}
	return os.Stat(name)
}

func (f *fakeFS) RemoveAll(path string) error {
	f.removeAllCalls = append(f.removeAllCalls, path)
	if f.missingPaths[path] {
		return nil
	}
	return os.RemoveAll(path)
}

func (f *fakeFS) MkdirAll(path string, perm os.FileMode) error {
	f.mkdirAllCalls = append(f.mkdirAllCalls, path)
	return os.MkdirAll(path, perm)
}

func (f *fakeFS) ReadDir(name string) ([]os.DirEntry, error) {
	f.readDirCalls = append(f.readDirCalls, name)
	return os.ReadDir(name)
}

// TestGitWorktree_Setup_RoutesMkdirAllThroughFS proves the seam: Setup's
// "ensure worktrees dir exists" step goes through g.fs.MkdirAll, not os.*
// directly. This is the guarantee that v2 can swap in a remote FS so a
// worktree dir on a distant host is created on the right machine.
func TestGitWorktree_Setup_RoutesMkdirAllThroughFS(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	require.NoError(t, os.Setenv("HOME", tempHome))
	defer func() { _ = os.Setenv("HOME", originalHome) }()

	// A real repo so resolveWorktreePaths / Setup's git commands succeed.
	repoPath := filepath.Join(t.TempDir(), "repo")
	mustRunGit(t, "", "init", repoPath)
	mustRunGit(t, repoPath, "config", "user.name", "T")
	mustRunGit(t, repoPath, "config", "user.email", "t@e.com")
	mustRunGit(t, repoPath, "commit", "--allow-empty", "-m", "init")

	fsys := &fakeFS{}
	executor := cmdtest.MockCmdExec{
		// Route git commands to the real executor — we only care about FS routing here.
		RunFunc:             func(c *exec.Cmd) error { return c.Run() },
		OutputFunc:          func(c *exec.Cmd) ([]byte, error) { return c.Output() },
		CombinedOutputFunc:  func(c *exec.Cmd) ([]byte, error) { return c.CombinedOutput() },
	}

	g, _, err := NewGitWorktreeWithDeps(repoPath, "sess", executor, fsys)
	require.NoError(t, err)

	require.NoError(t, g.Setup())

	// Setup's first step is MkdirAll on the worktrees dir. It must go through
	// the injected FS — proving the seam — not os.MkdirAll directly.
	require.NotEmpty(t, fsys.mkdirAllCalls, "Setup must route MkdirAll through g.fs")
	assert.Contains(t, fsys.mkdirAllCalls[0], "worktrees")
}

// TestGitWorktree_IsValidWorktree_RoutesStatThroughFS proves IsValidWorktree
// checks the worktree path via g.fs.Stat (not os.Stat), so a remote worktree
// is validated on the right host.
func TestGitWorktree_IsValidWorktree_RoutesStatThroughFS(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	require.NoError(t, os.Setenv("HOME", tempHome))
	defer func() { _ = os.Setenv("HOME", originalHome) }()

	repoPath := filepath.Join(t.TempDir(), "repo")
	mustRunGit(t, "", "init", repoPath)
	mustRunGit(t, repoPath, "config", "user.name", "T")
	mustRunGit(t, repoPath, "config", "user.email", "t@e.com")
	mustRunGit(t, repoPath, "commit", "--allow-empty", "-m", "init")

	executor := cmdtest.MockCmdExec{
		RunFunc:            func(c *exec.Cmd) error { return c.Run() },
		OutputFunc:         func(c *exec.Cmd) ([]byte, error) { return c.Output() },
		CombinedOutputFunc: func(c *exec.Cmd) ([]byte, error) { return c.CombinedOutput() },
	}
	fsys := &fakeFS{}

	g, _, err := NewGitWorktreeWithDeps(repoPath, "sess", executor, fsys)
	require.NoError(t, err)
	require.NoError(t, g.Setup())

	// Reset recording so we only see IsValidWorktree's calls.
	fsys.statCalls = nil
	valid, err := g.IsValidWorktree()
	require.NoError(t, err)
	assert.True(t, valid)

	// IsValidWorktree stats the worktree path and its .git via g.fs.
	require.NotEmpty(t, fsys.statCalls, "IsValidWorktree must route Stat through g.fs")
	assert.Contains(t, fsys.statCalls[0], "worktrees")
}

// TestGitWorktree_WorktreeDirExists_AndRemoveWorktreeDir_RouteThroughFS proves
// the two methods Pause relies on go through g.fs, so a remote worktree's
// directory is checked/removed on the right host.
func TestGitWorktree_WorktreeDirExists_AndRemoveWorktreeDir_RouteThroughFS(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	require.NoError(t, os.Setenv("HOME", tempHome))
	defer func() { _ = os.Setenv("HOME", originalHome) }()

	repoPath := filepath.Join(t.TempDir(), "repo")
	mustRunGit(t, "", "init", repoPath)
	mustRunGit(t, repoPath, "config", "user.name", "T")
	mustRunGit(t, repoPath, "config", "user.email", "t@e.com")
	mustRunGit(t, repoPath, "commit", "--allow-empty", "-m", "init")

	executor := cmdtest.MockCmdExec{
		RunFunc:            func(c *exec.Cmd) error { return c.Run() },
		OutputFunc:         func(c *exec.Cmd) ([]byte, error) { return c.Output() },
		CombinedOutputFunc: func(c *exec.Cmd) ([]byte, error) { return c.CombinedOutput() },
	}
	fsys := &fakeFS{}

	g, _, err := NewGitWorktreeWithDeps(repoPath, "sess", executor, fsys)
	require.NoError(t, err)
	require.NoError(t, g.Setup())

	// WorktreeDirExists stats via g.fs.
	fsys.statCalls = nil
	assert.True(t, g.WorktreeDirExists())
	require.NotEmpty(t, fsys.statCalls, "WorktreeDirExists must route Stat through g.fs")
	assert.Equal(t, g.GetWorktreePath(), fsys.statCalls[0])

	// RemoveWorktreeDir removes via g.fs.
	fsys.removeAllCalls = nil
	require.NoError(t, g.RemoveWorktreeDir())
	require.NotEmpty(t, fsys.removeAllCalls, "RemoveWorktreeDir must route RemoveAll through g.fs")
	assert.Equal(t, g.GetWorktreePath(), fsys.removeAllCalls[0])

	// After removal, the dir is gone (proving the recorded call actually acted).
	assert.False(t, g.WorktreeDirExists())
}
