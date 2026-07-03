package git

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"claude-squad/cmd/cmd_test"
	cmd2 "claude-squad/cmd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeMergeRepo creates a real git repo with an initial commit on `main` and
// returns its absolute path. Used by merge integration tests.
func makeMergeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mrun(t, "", "init", "-b", "main", dir)
	mrun(t, dir, "config", "user.name", "Test")
	mrun(t, dir, "config", "user.email", "t@e.com")
	mrun(t, dir, "commit", "--allow-empty", "-m", "init")
	abs, err := filepath.Abs(dir)
	require.NoError(t, err)
	return abs
}

// makeMergeRepoTrunk creates a repo whose trunk is a NON-protected branch
// ("integration") so merge-into-trunk tests don't trip the protected-branch
// guard. The guard refuses main/master; tests that exercise a legitimate
// merge target use this helper.
func makeMergeRepoTrunk(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mrun(t, "", "init", "-b", "integration", dir)
	mrun(t, dir, "config", "user.name", "Test")
	mrun(t, dir, "config", "user.email", "t@e.com")
	mrun(t, dir, "commit", "--allow-empty", "-m", "init")
	abs, err := filepath.Abs(dir)
	require.NoError(t, err)
	return abs
}

func mrun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmdArgs := args
	if dir != "" {
		cmdArgs = append([]string{"-C", dir}, args...)
	}
	cmdArgs = append([]string{"git"}, cmdArgs...)
	out, err := exec.Command(cmdArgs[0], cmdArgs[1:]...).CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
}

// writeCommit creates or overwrites a file in the repo and commits it.
func writeCommit(t *testing.T, repo, file, content, msg string) {
	t.Helper()
	// Write the file via a temp script that interpolates content safely.
	// Using os.WriteFile would be cleaner but we keep it shell-based to match
	// the repo's git-test style; content is written via printf with %b so
	// escape sequences like \n are honoured.
	script := "cd " + repo + " && printf '%b' " + shellQuote(content) + " > " + file + " && git add " + file + " && git commit -q -m " + shellQuote(msg)
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	require.NoErrorf(t, err, "writeCommit: %s", out)
}

// shellQuote wraps a string in single quotes, escaping internal single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// TestMerger_CleanMerge proves the happy path: two source branches with
// disjoint changes merge cleanly into the target, status=Merged, no conflicts.
func TestMerger_CleanMerge(t *testing.T) {
	repo := makeMergeRepoTrunk(t)

	// Create two feature branches from integration, each touching a different file.
	mrun(t, repo, "branch", "feat-a")
	mrun(t, repo, "checkout", "feat-a")
	writeCommit(t, repo, "a.txt", "A\n", "feat-a")
	mrun(t, repo, "checkout", "integration")

	mrun(t, repo, "branch", "feat-b")
	mrun(t, repo, "checkout", "feat-b")
	writeCommit(t, repo, "b.txt", "B\n", "feat-b")
	mrun(t, repo, "checkout", "integration")

	m := NewMerger(cmd2.MakeExecutor())
	res, err := m.Merge(repo, "integration", []string{"feat-a", "feat-b"}, StrategyDefault)
	require.NoError(t, err)
	assert.Equal(t, MergeMerged, res.Status, "disjoint branches merge cleanly")
	assert.Empty(t, res.Conflicts)

	// Both files are now present on integration.
	mrun(t, repo, "checkout", "integration") // merger already checked out target, but be safe
	out, err := exec.Command("git", "-C", repo, "ls-files").Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), "a.txt")
	assert.Contains(t, string(out), "b.txt")
}

// TestMerger_ConflictDetected proves a real conflict is detected, the result
// carries the conflicted file, and the repo is left in the merging state
// (NOT auto-aborted) so a resolver can act.
func TestMerger_ConflictDetected(t *testing.T) {
	repo := makeMergeRepoTrunk(t)

	// integration has file.txt = "base"
	writeCommit(t, repo, "file.txt", "base\n", "base")

	// feat changes line 1 to "theirs"
	mrun(t, repo, "branch", "feat")
	mrun(t, repo, "checkout", "feat")
	writeCommit(t, repo, "file.txt", "theirs\n", "theirs")

	// integration changes line 1 to "ours" (diverges from feat's base)
	mrun(t, repo, "checkout", "integration")
	writeCommit(t, repo, "file.txt", "ours\n", "ours")

	m := NewMerger(cmd2.MakeExecutor())
	res, err := m.Merge(repo, "integration", []string{"feat"}, StrategyDefault)
	// A conflicting merge returns MergeConflict + a non-nil error (git merge
	// exits non-zero) but the result carries the conflict list. The contract:
	// Status=Conflict + Conflicts populated, repo left merging.
	_ = err // git merge exits non-zero on conflict; we care about the result
	assert.Equal(t, MergeConflict, res.Status)
	require.NotEmpty(t, res.Conflicts, "conflicted file must be reported")
	assert.Equal(t, "file.txt", res.Conflicts[0].File)

	// The repo is left in a merging state (not aborted): git status shows
	// "Unmerged paths".
	status, err := exec.Command("git", "-C", repo, "status", "--porcelain").Output()
	require.NoError(t, err)
	assert.Contains(t, string(status), "UU", "repo left in merging state, not auto-aborted")
}

// TestMerger_ProtectedBranchRefused proves the guard: merging INTO main is
// refused with ErrProtectedBranch, even though the Merger could otherwise do
// it. The kernel enforces this too, but the Merger defends in depth.
func TestMerger_ProtectedBranchRefused(t *testing.T) {
	repo := makeMergeRepo(t)
	mrun(t, repo, "branch", "feat")
	mrun(t, repo, "checkout", "feat")
	writeCommit(t, repo, "x.txt", "x\n", "x")
	mrun(t, repo, "checkout", "main")

	m := NewMerger(cmd2.MakeExecutor())
	_, err := m.Merge(repo, "main", []string{"feat"}, StrategyDefault)
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "protected") || strings.Contains(err.Error(), "main"),
		"error must mention protected branch: %v", err)
}

// TestMerger_RoutesViaExecutor proves the seam: Merge builds `git -C <repo>`
// commands and routes them through the injected Executor (not exec directly).
// This is the guarantee v2 needs to merge on a remote repo over SSH.
func TestMerger_RoutesViaExecutor(t *testing.T) {
	var got *exec.Cmd
	executor := cmd_test.MockCmdExec{
		CombinedOutputFunc: func(c *exec.Cmd) ([]byte, error) {
			got = c
			return []byte("Already up to date.\n"), nil
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) {
			return []byte(""), nil
		},
	}

	m := NewMerger(executor)
	_, _ = m.Merge("/some/repo", "target", []string{"src"}, StrategyDefault)

	require.NotNil(t, got, "command must be routed via the executor")
	assert.Equal(t, "git", got.Args[0])
	assert.Equal(t, "/some/repo", got.Args[2], "repo path passed via -C, not cwd")
}

// TestMerger_RequiresSourceBranches proves the validation guard.
func TestMerger_RequiresSourceBranches(t *testing.T) {
	m := NewMerger(cmd2.MakeExecutor())
	_, err := m.Merge("/repo", "main", nil, StrategyDefault)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source branch")
}

// TestMerger_NonexistentSourceErrors proves a bad source branch surfaces as
// an error without claiming a conflict (no conflicted files in the index).
func TestMerger_NonexistentSourceErrors(t *testing.T) {
	repo := makeMergeRepoTrunk(t)
	m := NewMerger(cmd2.MakeExecutor())
	res, err := m.Merge(repo, "integration", []string{"does-not-exist"}, StrategyDefault)
	require.Error(t, err)
	assert.Equal(t, MergeConflict, res.Status, "non-merge exit still reports Conflict status")
	assert.Empty(t, res.Conflicts, "no conflicted files for a missing source branch")
}

// TestMerger_UnimplementedStrategy proves the reserved-strategy guard.
func TestMerger_UnimplementedStrategy(t *testing.T) {
	repo := makeMergeRepoTrunk(t)
	m := NewMerger(cmd2.MakeExecutor())
	_, err := m.Merge(repo, "integration", []string{"feat"}, StrategyOurs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not implemented")
}

// TestMerger_NilExecutorDefaultsToLocal proves a nil executor is tolerated
// (defaults to the local executor) — convenience for callers that don't care.
func TestMerger_NilExecutorDefaultsToLocal(t *testing.T) {
	repo := makeMergeRepoTrunk(t)
	mrun(t, repo, "branch", "feat")
	mrun(t, repo, "checkout", "feat")
	writeCommit(t, repo, "c.txt", "C\n", "c")
	mrun(t, repo, "checkout", "integration")

	m := NewMerger(nil)
	res, err := m.Merge(repo, "integration", []string{"feat"}, StrategyDefault)
	require.NoError(t, err)
	assert.Equal(t, MergeMerged, res.Status)
}
