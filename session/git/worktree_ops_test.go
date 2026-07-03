package git

import (
	"claude-squad/cmd"
	cmdtest "claude-squad/cmd/cmd_test"
	"claude-squad/session/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupFromExistingBranch_RemovesOrphanedDirectory(t *testing.T) {
	tempHome := t.TempDir()
	originalHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", tempHome); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	defer func() {
		_ = os.Setenv("HOME", originalHome)
	}()

	repoPath := filepath.Join(t.TempDir(), "repo")
	mustRunGit(t, "", "init", repoPath)
	mustRunGit(t, repoPath, "config", "user.name", "Test User")
	mustRunGit(t, repoPath, "config", "user.email", "test@example.com")

	readmePath := filepath.Join(repoPath, "README.md")
	if err := os.WriteFile(readmePath, []byte("hello\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}

	mustRunGit(t, repoPath, "add", "README.md")
	mustRunGit(t, repoPath, "commit", "-m", "initial")
	mustRunGit(t, repoPath, "branch", "feature/test")

	worktreeDir := filepath.Join(tempHome, ".claude-squad", "worktrees")
	worktreePath := filepath.Join(worktreeDir, "feature-test")
	if err := os.MkdirAll(worktreePath, 0755); err != nil {
		t.Fatalf("mkdir orphaned worktree: %v", err)
	}

	junkPath := filepath.Join(worktreePath, "orphan.txt")
	if err := os.WriteFile(junkPath, []byte("orphaned\n"), 0644); err != nil {
		t.Fatalf("write orphan marker: %v", err)
	}

	g := &GitWorktree{
		repoPath:         repoPath,
		worktreePath:     worktreePath,
		branchName:       "feature/test",
		isExistingBranch: true,
		cmdExec:          cmd.MakeExecutor(),
		fs:               fs.LocalFS{},
		worktreeDir:      worktreeDir,
	}

	if err := g.Setup(); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}

	if _, err := os.Stat(junkPath); !os.IsNotExist(err) {
		t.Fatalf("orphan marker still exists after Setup, err = %v", err)
	}

	if valid, err := g.IsValidWorktree(); err != nil {
		t.Fatalf("IsValidWorktree() error = %v", err)
	} else if !valid {
		t.Fatal("expected Setup() to recreate a valid worktree")
	}

	currentBranch := mustRunGit(t, worktreePath, "branch", "--show-current")
	if currentBranch != "feature/test\n" {
		t.Fatalf("current branch = %q, want %q", currentBranch, "feature/test\n")
	}
}

func mustRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmdArgs := args
	if dir != "" {
		cmdArgs = append([]string{"-C", dir}, args...)
	}

	cmd := exec.Command("git", cmdArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return string(output)
}

// makeTempRepoWithCommit creates a real git repository (with one empty commit)
// at a temp dir and returns its absolute path.
func makeTempRepoWithCommit(t *testing.T, name string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), name)
	mustRunGit(t, "", "init", repo)
	mustRunGit(t, repo, "config", "user.name", "T")
	mustRunGit(t, repo, "config", "user.email", "t@e.com")
	mustRunGit(t, repo, "commit", "--allow-empty", "-m", "init")
	return repo
}

// branchExists reports whether the given branch exists in repo.
func branchExists(t *testing.T, repo, branch string) bool {
	t.Helper()
	out := mustRunGit(t, repo, "branch", "--list", branch)
	return strings.TrimSpace(out) != ""
}

// anyCmdContains reports whether any of the recorded command strings contains
// needle.
func anyCmdContains(cmds []string, needle string) bool {
	for _, c := range cmds {
		if strings.Contains(c, needle) {
			return true
		}
	}
	return false
}

// TestCleanupWorktrees_MultiRepoDeletesBranchFromEachRepo is the regression
// test for dette #1: the old CleanupWorktrees ran `git worktree list` and
// `git branch -D` WITHOUT -C, operating on whatever repo was in the cwd — a
// latent multi-repo bug. With two independent repos each holding a cs2
// worktree under a shared worktrees dir, the sweep must delete each worktree's
// branch from its OWN repo, not cwd's. We run it from a neutral non-repo cwd
// so any -C-less `git` would fail to find a repo (the old behaviour deleted
// nothing and left branches orphaned).
func TestCleanupWorktrees_MultiRepoDeletesBranchFromEachRepo(t *testing.T) {
	worktreesDir := t.TempDir()
	repoA := makeTempRepoWithCommit(t, "repoA")
	repoB := makeTempRepoWithCommit(t, "repoB")

	branchA := "cs2/task-a"
	branchB := "cs2/task-b"
	wtA := filepath.Join(worktreesDir, "wtA")
	wtB := filepath.Join(worktreesDir, "wtB")
	mustRunGit(t, repoA, "worktree", "add", "-b", branchA, wtA)
	mustRunGit(t, repoB, "worktree", "add", "-b", branchB, wtB)

	// Run from a neutral non-repo cwd to prove the sweep is cwd-independent.
	origWd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(t.TempDir()))
	defer func() { _ = os.Chdir(origWd) }()

	require.NoError(t, CleanupWorktreesWithDeps(cmd.MakeExecutor(), fs.LocalFS{}, worktreesDir))

	// Both worktrees gone.
	_, err = os.Stat(wtA)
	assert.True(t, os.IsNotExist(err), "wtA should be removed")
	_, err = os.Stat(wtB)
	assert.True(t, os.IsNotExist(err), "wtB should be removed")
	// Both branches deleted from their OWN repos (not cwd's).
	assert.False(t, branchExists(t, repoA, branchA), "branchA must be deleted from repoA")
	assert.False(t, branchExists(t, repoB, branchB), "branchB must be deleted from repoB")
}

// TestCleanupWorktrees_RoutesThroughDeps proves the seam closure: cleanup
// reads/removes via the injected FS and runs git via the injected Executor
// (no direct os.* or exec.Command), and every git command carries -C
// (repo-aware, never cwd). This is the guarantee v2 needs to sweep a remote
// host's worktrees through an SSH executor/FS.
func TestCleanupWorktrees_RoutesThroughDeps(t *testing.T) {
	repo := makeTempRepoWithCommit(t, "repo")
	worktreesDir := t.TempDir()
	wt := filepath.Join(worktreesDir, "wt")
	branch := "cs2/task"
	mustRunGit(t, repo, "worktree", "add", "-b", branch, wt)

	fsys := &fakeFS{}
	var cmds []string
	record := func(c *exec.Cmd) { cmds = append(cmds, strings.Join(c.Args, " ")) }
	exec := cmdtest.MockCmdExec{
		RunFunc:            func(c *exec.Cmd) error { record(c); return c.Run() },
		OutputFunc:         func(c *exec.Cmd) ([]byte, error) { record(c); return c.Output() },
		CombinedOutputFunc: func(c *exec.Cmd) ([]byte, error) { record(c); return c.CombinedOutput() },
	}

	require.NoError(t, CleanupWorktreesWithDeps(exec, fsys, worktreesDir))

	// ReadDir + RemoveAll went through the injected FS.
	assert.Contains(t, fsys.readDirCalls, worktreesDir)
	assert.NotEmpty(t, fsys.removeAllCalls, "cleanup must route RemoveAll through fsys")

	// git commands went through the injected executor.
	require.NotEmpty(t, cmds, "cleanup must route git through cmdExec")
	for _, c := range cmds {
		assert.Contains(t, c, "git -C", "git command must be repo-aware (-C): %s", c)
	}
	assert.True(t, anyCmdContains(cmds, "worktree remove"), "must remove the worktree")
	assert.True(t, anyCmdContains(cmds, "branch -D"), "must delete the branch")
	assert.False(t, anyCmdContains(cmds, "worktree list"), "must not use the old cwd-less `git worktree list`")
}

// TestCleanupWorktrees_OrphanedNonGitDirRemoved proves a directory under the
// worktrees dir that is not a git worktree (corrupt/orphaned) is still removed,
// and without error — git -C <dir> fails to resolve a repo, so cleanup falls
// back to a raw RemoveAll.
func TestCleanupWorktrees_OrphanedNonGitDirRemoved(t *testing.T) {
	worktreesDir := t.TempDir()
	junk := filepath.Join(worktreesDir, "junk")
	require.NoError(t, os.MkdirAll(filepath.Join(junk, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(junk, "f"), []byte("x"), 0o644))

	fsys := &fakeFS{}
	require.NoError(t, CleanupWorktreesWithDeps(cmd.MakeExecutor(), fsys, worktreesDir))

	_, err := os.Stat(junk)
	assert.True(t, os.IsNotExist(err), "orphaned non-git dir must be removed")
	assert.NotEmpty(t, fsys.removeAllCalls, "orphaned dir removed via fsys")
}
