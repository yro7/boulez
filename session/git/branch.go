package git

import (
	"claude-squad/cmd"
	"fmt"
	"os/exec"
)

// ErrBranchNotFound is returned when a branch is required to pre-exist but is
// absent (both locally and on the remote). The wire transport maps it to the
// BRANCH_NOT_FOUND code so a client (or an LLM tool) can branch on it without
// parsing the message. It is also returned by setupFromExistingBranch when a
// restored worktree's branch has vanished.
type ErrBranchNotFound struct {
	Name string
}

func (e ErrBranchNotFound) Error() string {
	return fmt.Sprintf("branch %s not found locally or on remote", e.Name)
}

// branchExists reports whether branch exists in repo, either as a local head
// or as a remote-tracking branch on origin. Mirrors the two show-ref probes
// used by setupFromExistingBranch so EnsureBranch and the worktree setup agree
// on what "exists" means.
func branchExistsInRepo(ce cmd.Executor, repo, branch string) (bool, error) {
	if _, err := ce.Output(exec.Command("git", "-C", repo, "show-ref", "--verify", fmt.Sprintf("refs/heads/%s", branch))); err == nil {
		return true, nil
	}
	if _, err := ce.Output(exec.Command("git", "-C", repo, "show-ref", "--verify", fmt.Sprintf("refs/remotes/origin/%s", branch))); err == nil {
		return true, nil
	}
	return false, nil
}

// EnsureBranch ensures branch exists in repo. If the branch already exists
// (locally or on the remote) it is a no-op. If it does not exist:
//   - when mustExist is true, EnsureBranch returns ErrBranchNotFound (the
//     caller required a pre-existing branch, e.g. --branch-existing to resume
//     work on a branch that must already be there).
//   - when mustExist is false (the default), EnsureBranch creates the branch
//     from HEAD so a subsequent worktree setup on that branch succeeds. This
//     is the orchestrator-friendly default: deterministic branch names without
//     requiring the caller to create the branch externally first.
//
// EnsureBranch is the single place that decides the "create if absent"
// policy; the worktree setup layer then always finds the branch present and
// proceeds with the existing-branch path. Keeping the policy here (not in the
// worktree) means the worktree never needs a branchMustExist flag threaded
// through its constructors.
func EnsureBranch(repo, branch string, mustExist bool) error {
	ce := cmd.MakeExecutor()
	exists, err := branchExistsInRepo(ce, repo, branch)
	if err != nil {
		return fmt.Errorf("check branch %s: %w", branch, err)
	}
	if exists {
		return nil
	}
	if mustExist {
		return ErrBranchNotFound{Name: branch}
	}
	if err := ce.Run(exec.Command("git", "-C", repo, "branch", branch, "HEAD")); err != nil {
		return fmt.Errorf("create branch %s: %w", branch, err)
	}
	return nil
}
