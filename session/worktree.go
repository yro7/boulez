package session

import "claude-squad/session/git"

// Worktree is the polymorphic worktree surface an Instance operates on.
//
// A Worker instance gets a real git worktree (a *git.GitWorktree, which
// satisfies this interface). An Orchestrator instance gets a headless
// no-op worktree (headlessWorktree) — it supervises the fleet, it does not
// edit code in a repo, so every git operation is a no-op and there is no
// branch, no base commit, no diff.
//
// The Worker/Orchestrator distinction lives ENTIRELY behind this interface.
// No caller does `if kind == ...`: the difference is bound once, at Start
// time, in the factory that picks which Worktree implementation to install.
// This is the deep-module move — the seam is a single interface, and the two
// implementations hide large behaviour differences behind it.
type Worktree interface {
	// Lifecycle.
	Setup() error
	Cleanup() error
	Remove() error
	Prune() error

	// State queries.
	IsDirty() (bool, error)
	IsValidWorktree() (bool, error)
	WorktreeDirExists() bool
	IsBranchCheckedOut() (bool, error)
	IsExistingBranch() bool

	// Mutations.
	RemoveWorktreeDir() error
	CommitChanges(commitMessage string) error
	PushChanges(commitMessage string, open bool) error

	// Diff (nil/empty for a headless worktree).
	Diff() *git.DiffStats
	DiffNumstat() *git.DiffStats

	// Accessors. For a headless worktree these return "" / the control dir,
	// never a real repo path.
	GetWorktreePath() string
	GetBranchName() string
	GetRepoPath() string
	GetRepoName() string
	GetBaseCommitSHA() string
}
