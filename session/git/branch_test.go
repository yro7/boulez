package git

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureBranch_CreatesIfAbsent proves the orchestrator-friendly default:
// a missing branch is created from HEAD so a subsequent spawn on that branch
// succeeds without the caller pre-creating it.
func TestEnsureBranch_CreatesIfAbsent(t *testing.T) {
	repo := makeTempRepoWithCommit(t, "repo")

	require.NoError(t, EnsureBranch(repo, "feat-new", false))

	// The branch now exists locally.
	out, err := exec.Command("git", "-C", repo, "show-ref", "--verify", "refs/heads/feat-new").CombinedOutput()
	require.NoError(t, err, "branch should exist: %s", out)
}

// TestEnsureBranch_NoopIfExisting proves EnsureBranch never clobbers an
// existing branch (local or remote-tracking) — it's idempotent.
func TestEnsureBranch_NoopIfExisting(t *testing.T) {
	repo := makeTempRepoWithCommit(t, "repo")
	mustRunGit(t, repo, "branch", "already-there")

	headBefore, err := exec.Command("git", "-C", repo, "rev-parse", "already-there").Output()
	require.NoError(t, err)

	require.NoError(t, EnsureBranch(repo, "already-there", false))

	headAfter, err := exec.Command("git", "-C", repo, "rev-parse", "already-there").Output()
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(string(headBefore)), strings.TrimSpace(string(headAfter)),
		"existing branch must not be moved")
}

// TestEnsureBranch_MustExistFailsIfAbsent proves --branch-existing refuses an
// absent branch with the typed ErrBranchNotFound (the old behaviour,
// restorable for callers that want to resume an existing branch).
func TestEnsureBranch_MustExistFailsIfAbsent(t *testing.T) {
	repo := makeTempRepoWithCommit(t, "repo")

	err := EnsureBranch(repo, "ghost", true)
	require.Error(t, err)
	var nf ErrBranchNotFound
	assert.ErrorAs(t, err, &nf)
	assert.Equal(t, "ghost", nf.Name)
}

// TestEnsureBranch_MustExistSucceedsIfPresent proves --branch-existing accepts
// a branch that already exists (the resume case).
func TestEnsureBranch_MustExistSucceedsIfPresent(t *testing.T) {
	repo := makeTempRepoWithCommit(t, "repo")
	mustRunGit(t, repo, "branch", "resume-me")

	require.NoError(t, EnsureBranch(repo, "resume-me", true))
}
