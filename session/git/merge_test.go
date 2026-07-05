package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yro7/boulez/cmd/cmd_test"
	cmd2 "github.com/yro7/boulez/cmd"
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
	assert.Empty(t, res.WorktreePath, "clean merge removes its throwaway worktree")

	// The integration branch ref is updated to the merged commit. Verify by
	// checking the files are present on the branch's tree (not the user's
	// working tree — the merge no longer touches the main checkout).
	out, err := exec.Command("git", "-C", repo, "ls-tree", "-r", "--name-only", "integration").Output()
	require.NoError(t, err)
	assert.Contains(t, string(out), "a.txt")
	assert.Contains(t, string(out), "b.txt")

	// The user's main checkout (still on integration from the test setup) was
	// NOT switched or dirtied by the merge: the working tree matches HEAD
	// before the merge's ref update. We assert no MERGE_HEAD is left behind.
	_, err = os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD"))
	assert.True(t, os.IsNotExist(err), "main checkout not left in a merging state")
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
	// Status=Conflict + Conflicts populated + WorktreePath set (the throwaway
	// worktree is left for a resolver).
	require.Error(t, err, "conflicting merge returns an error")
	assert.Equal(t, MergeConflict, res.Status)
	require.NotEmpty(t, res.Conflicts, "conflicted file must be reported")
	assert.Equal(t, "file.txt", res.Conflicts[0].File)
	require.NotEmpty(t, res.WorktreePath, "conflict leaves the throwaway worktree for a resolver")
	t.Cleanup(func() {
		_, _ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", res.WorktreePath).CombinedOutput()
		_ = os.RemoveAll(res.WorktreePath)
	})

	// The throwaway worktree (not the main checkout) is in the merging state.
	status, err := exec.Command("git", "-C", res.WorktreePath, "status", "--porcelain").Output()
	require.NoError(t, err)
	assert.Contains(t, string(status), "UU", "throwaway worktree left in merging state, not auto-aborted")

	// The main checkout was NOT touched: no MERGE_HEAD left behind.
	_, err = os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD"))
	assert.True(t, os.IsNotExist(err), "main checkout not left in a merging state")
}

// TestMerger_MergeTrunk_AcceptsMain proves the trunk-allowed path: a clean
// merge INTO main succeeds via MergeTrunk, whereas the equivalent Merge call
// is still refused (the guard remains hard for the regular path). This is the
// contract Land relies on: only MergeTrunk can land onto a trunk.
func TestMerger_MergeTrunk_AcceptsMain(t *testing.T) {
	repo := makeMergeRepo(t) // trunk = main
	mrun(t, repo, "branch", "feat")
	mrun(t, repo, "checkout", "feat")
	writeCommit(t, repo, "new.txt", "N\n", "new")
	mrun(t, repo, "checkout", "main")

	m := NewMerger(cmd2.MakeExecutor())
	res, err := m.MergeTrunk(repo, "main", []string{"feat"}, StrategyDefault)
	require.NoError(t, err, "MergeTrunk accepts main")
	assert.Equal(t, MergeMerged, res.Status)

	// The regular path still refuses the same target (guard unchanged).
	mrun(t, repo, "checkout", "main")
	_, err = m.Merge(repo, "main", []string{"feat"}, StrategyDefault)
	require.Error(t, err, "Merge still refuses main")
}

// TestMerger_MergeTrunk_ConflictNotAborted proves MergeTrunk leaves the repo
// in the merging state on conflict (no silent --abort), carrying the
// conflicted file — same contract as the regular Merge conflict path.
func TestMerger_MergeTrunk_ConflictNotAborted(t *testing.T) {
	repo := makeMergeRepo(t)
	writeCommit(t, repo, "file.txt", "base\n", "base")

	mrun(t, repo, "branch", "feat")
	mrun(t, repo, "checkout", "feat")
	writeCommit(t, repo, "file.txt", "theirs\n", "theirs")

	mrun(t, repo, "checkout", "main")
	writeCommit(t, repo, "file.txt", "ours\n", "ours")

	m := NewMerger(cmd2.MakeExecutor())
	res, err := m.MergeTrunk(repo, "main", []string{"feat"}, StrategyDefault)
	require.Error(t, err, "conflicting merge returns an error")
	assert.Equal(t, MergeConflict, res.Status)
	require.NotEmpty(t, res.Conflicts, "conflicted file must be reported")
	assert.Equal(t, "file.txt", res.Conflicts[0].File)
	require.NotEmpty(t, res.WorktreePath, "conflict leaves the throwaway worktree for a resolver")
	t.Cleanup(func() {
		_, _ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", res.WorktreePath).CombinedOutput()
		_ = os.RemoveAll(res.WorktreePath)
	})

	status, err := exec.Command("git", "-C", res.WorktreePath, "status", "--porcelain").Output()
	require.NoError(t, err)
	assert.Contains(t, string(status), "UU", "throwaway worktree left in merging state, not auto-aborted")

	_, err = os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD"))
	assert.True(t, os.IsNotExist(err), "main checkout not left in a merging state")
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

// TestMerger_DoesNotMutateMainCheckout is the isolation regression: a merge
// must NOT switch, dirty, or leave MERGE_HEAD in the user's main checkout.
// Before the fix, mergeInto ran `git checkout <target>` in repoPath directly,
// switching the user's branch and leaving a merge-in-progress on conflict.
// The daemon has no repo/workspace of its own (inversion north-star); merges
// run in an isolated throwaway worktree and only the target branch ref moves.
func TestMerger_DoesNotMutateMainCheckout(t *testing.T) {
	repo := makeMergeRepoTrunk(t) // trunk = integration

	// Set up a conflicting feat branch so the merge leaves a worktree behind
	// (the path most likely to leak state into the main checkout pre-fix).
	writeCommit(t, repo, "file.txt", "base\n", "base")
	mrun(t, repo, "branch", "feat")
	mrun(t, repo, "checkout", "feat")
	writeCommit(t, repo, "file.txt", "theirs\n", "theirs")
	mrun(t, repo, "checkout", "integration")
	writeCommit(t, repo, "file.txt", "ours\n", "ours")

	// Record the main checkout's state before the merge.
	beforeBranch := strings.TrimSpace(mustOutput(t, repo, "branch", "--show-current"))
	beforeHead := strings.TrimSpace(mustOutput(t, repo, "rev-parse", "HEAD"))

	m := NewMerger(cmd2.MakeExecutor())
	res, err := m.Merge(repo, "integration", []string{"feat"}, StrategyDefault)
	require.Error(t, err, "conflicting merge")
	require.Equal(t, MergeConflict, res.Status)
	require.NotEmpty(t, res.WorktreePath)
	t.Cleanup(func() {
		_, _ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", res.WorktreePath).CombinedOutput()
		_ = os.RemoveAll(res.WorktreePath)
	})

	// The main checkout's branch and HEAD are unchanged.
	afterBranch := strings.TrimSpace(mustOutput(t, repo, "branch", "--show-current"))
	afterHead := strings.TrimSpace(mustOutput(t, repo, "rev-parse", "HEAD"))
	assert.Equal(t, beforeBranch, afterBranch, "main checkout branch not switched")
	assert.Equal(t, beforeHead, afterHead, "main checkout HEAD not moved")

	// No merge state left in the main checkout.
	_, statErr := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD"))
	assert.True(t, os.IsNotExist(statErr), "no MERGE_HEAD left in main checkout")
}

func mustOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", full...).Output()
	require.NoErrorf(t, err, "git %v: %s", args, out)
	return string(out)
}
