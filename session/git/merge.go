package git

import (
	"claude-squad/cmd"
	"fmt"
	"os/exec"
	"strings"
)

// MergeStatus is the outcome of a merge attempt.
type MergeStatus int

const (
	// MergeMerged means the sources merged cleanly into the target.
	MergeMerged MergeStatus = iota
	// MergeConflict means the merge left conflicts in the working tree. The
	// repo is left in the merging state (NOT auto-aborted) so a resolver
	// (agent or human) can inspect and resolve. The Merger never forces
	// `--abort` silently — that would discard information.
	MergeConflict
)

// Strategy selects a merge strategy. v1 implements only StrategyDefault
// (plain `git merge`); the others are reserved for future
// non-deterministic resolution (ours/theirs, LLM-aided).
type Strategy int

const (
	StrategyDefault Strategy = iota
	StrategyOurs   // reserved
	StrategyTheirs  // reserved
)

// Conflict describes one conflicted file in a failed merge.
type Conflict struct {
	// File is the path of the conflicted file, repo-relative.
	File string
	// Ours is the version on the target branch (empty when not extractable).
	Ours string
	// Theirs is the version from the source branch (empty when not extractable).
	Theirs string
}

// MergeResult is the outcome of Merger.Merge.
type MergeResult struct {
	Status    MergeStatus
	Conflicts []Conflict
	// Message is a human-readable summary (e.g. the merge output), for logs
	// and for an orchestrator's context.
	Message string
}

// Merger is the abstraction over merging one or more source branches into a
// target branch of a repository. It is repo-aware (uses `git -C <repo>`,
// never cwd) and transport-agnostic (commands run via the injected
// Executor, so a remote repo's merges route over SSH in v2).
//
// v1 is deterministic: a clean merge succeeds, a conflicting merge fails
// with Status=Conflict and the repo left for a resolver. Agent-aided
// conflict resolution (a worker spawned to resolve) is a Shape B concern
// that consumes this abstraction — it does not live here.
type Merger interface {
	// Merge checks out targetBranch in repoPath and merges sourceBranches into
	// it with the given strategy. Returns MergeMerged on success or
	// MergeConflict (with the list of conflicted files) on a conflicting merge.
	// The repo is never left in an aborted state on conflict.
	Merge(repoPath, targetBranch string, sourceBranches []string, strategy Strategy) (MergeResult, error)

	// MergeTrunk merges sourceBranches into targetBranch, which MAY be a trunk
	// (main/master). It exists ONLY for the Land syscall (a top-level explicit
	// request to land onto the trunk). The regular Merge refuses trunks; this
	// path lifts that single guard for the explicit land case. The host-current-
	// branch guard is NOT applied here (it lives in the kernel, which knows the
	// host repo). On conflict, the repo is left in the merging state.
	//
	// Callers other than the top-level Land syscall MUST NOT use this method —
	// workers and orchestrators go through Merge, which defends the trunk in
	// depth. The kernel enforces who may call Land, so a misbehaving client
	// cannot reach this path.
	MergeTrunk(repoPath, targetBranch string, sourceBranches []string, strategy Strategy) (MergeResult, error)
}

// defaultMerger is the v1 Merger: deterministic git merge.
type defaultMerger struct {
	cmdExec cmd.Executor
}

// NewMerger returns the v1 deterministic Merger backed by the given executor.
// A nil executor defaults to the local executor.
func NewMerger(cmdExec cmd.Executor) Merger {
	if cmdExec == nil {
		cmdExec = cmd.MakeExecutor()
	}
	return &defaultMerger{cmdExec: cmdExec}
}

func (m *defaultMerger) Merge(repoPath, targetBranch string, sourceBranches []string, strategy Strategy) (MergeResult, error) {
	if err := mergeValidate(repoPath, targetBranch, sourceBranches, strategy); err != nil {
		return MergeResult{}, err
	}

	// Guard: refuse protected branches. The kernel enforces the same guard at
	// a higher level (so a misbehaving client cannot bypass it), but the
	// Merger defends in depth — it is the last line before mutating git.
	if isProtectedBranch(targetBranch) {
		return MergeResult{Status: MergeConflict, Message: "protected branch"}, ErrProtectedBranch{Branch: targetBranch}
	}

	return m.mergeInto(repoPath, targetBranch, sourceBranches, strategy)
}

// MergeTrunk is the trunk-allowed variant of Merge. See the Merger interface
// doc for the contract: this path is reserved for the top-level Land syscall
// and lifts ONLY the conventional-trunk guard. The host-current-branch guard
// is NOT applied here — it lives in the kernel, which knows the host repo.
func (m *defaultMerger) MergeTrunk(repoPath, targetBranch string, sourceBranches []string, strategy Strategy) (MergeResult, error) {
	if err := mergeValidate(repoPath, targetBranch, sourceBranches, strategy); err != nil {
		return MergeResult{}, err
	}
	return m.mergeInto(repoPath, targetBranch, sourceBranches, strategy)
}

// mergeValidate validates the shared preconditions of Merge and MergeTrunk.
// Kept separate so both paths enforce the same input rules (DRY).
func mergeValidate(repoPath, targetBranch string, sourceBranches []string, strategy Strategy) error {
	if repoPath == "" {
		return fmt.Errorf("merge: repoPath is required")
	}
	if targetBranch == "" {
		return fmt.Errorf("merge: targetBranch is required")
	}
	if len(sourceBranches) == 0 {
		return fmt.Errorf("merge: at least one source branch is required")
	}
	if strategy != StrategyDefault {
		// Reserved for future work; v1 only implements the default strategy.
		return fmt.Errorf("merge: strategy %d not implemented in v1 (only StrategyDefault)", strategy)
	}
	return nil
}

// mergeInto is the shared body of Merge and MergeTrunk: it checks out the
// target branch and merges the sources with the given strategy. On a clean
// merge it returns MergeMerged; on a conflicting merge it returns
// MergeConflict (with the list of conflicted files) and leaves the repo in
// the merging state (never auto-aborts). Guards that differ between the two
// callers (trunk protection) are applied by the respective entrypoints.
func (m *defaultMerger) mergeInto(repoPath, targetBranch string, sourceBranches []string, strategy Strategy) (MergeResult, error) {
	// Checkout the target branch. This is the branch the orchestrator merges
	// INTO; sources merge into it.
	if out, err := m.runGit(repoPath, "checkout", targetBranch); err != nil {
		return MergeResult{Status: MergeConflict, Message: out}, fmt.Errorf("merge: checkout target %q: %s: %w", targetBranch, out, err)
	}

	// Merge the sources. Use --no-edit so a clean merge never blocks on an
	// editor. On conflict, git exits non-zero and leaves the repo in the
	// merging state (conflicted files marked in the index).
	args := []string{"merge", "--no-edit"}
	args = append(args, sourceBranches...)
	out, err := m.runGit(repoPath, args...)
	if err == nil {
		return MergeResult{Status: MergeMerged, Message: out}, nil
	}

	// Non-zero exit: either a conflict or a fast-forward/other failure.
	// Detect conflicted files via the index status.
	conflicts, cErr := m.conflictedFiles(repoPath)
	if cErr != nil {
		// Could not inspect the index — return what we have.
		return MergeResult{Status: MergeConflict, Conflicts: nil, Message: out}, fmt.Errorf("merge: failed and could not inspect conflicts: %s: %w", out, err)
	}
	if len(conflicts) == 0 {
		// Non-zero exit but no conflicted files — some other failure (e.g.
		// a source branch didn't exist). Surface the error without claiming
		// a conflict.
		return MergeResult{Status: MergeConflict, Conflicts: nil, Message: out}, fmt.Errorf("merge: git merge failed: %s: %w", out, err)
	}
	return MergeResult{Status: MergeConflict, Conflicts: conflicts, Message: out}, nil
}

// runGit runs `git -C repoPath args...` and returns the combined output + error.
func (m *defaultMerger) runGit(repoPath string, args ...string) (string, error) {
	full := append([]string{"-C", repoPath}, args...)
	c := exec.Command("git", full...)
	out, err := m.cmdExec.CombinedOutput(c)
	return string(out), err
}

// conflictedFiles returns the list of files marked as conflicted in the index
// (Unmerged entries), via `git -C <repo> diff --name-only --diff-filter=U`.
// Each conflict's Ours/Theirs is left empty in v1 — extracting per-side
// content is the resolver's job (Shape B), not the deterministic merger's.
func (m *defaultMerger) conflictedFiles(repoPath string) ([]Conflict, error) {
	c := exec.Command("git", "-C", repoPath, "diff", "--name-only", "--diff-filter=U")
	out, err := m.cmdExec.Output(c)
	if err != nil {
		return nil, fmt.Errorf("list conflicted files: %w", err)
	}
	var conflicts []Conflict
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		conflicts = append(conflicts, Conflict{File: line})
	}
	return conflicts, nil
}

// protectedBranches is the set of branch names the Merger refuses to merge
// INTO. In v1 this is the conventional trunk names. The kernel applies the
// same guard (and additionally refuses the repo host's checked-out branch),
// but the Merger defends in depth. Configurable lists are deferred.
var protectedBranches = map[string]bool{
	"main":   true,
	"master": true,
}

func isProtectedBranch(branch string) bool {
	return protectedBranches[strings.ToLower(branch)]
}

// ErrProtectedBranch is returned when a merge targets a protected branch.
// Typed so the kernel/transport can map it to a PROTECTED_BRANCH error code.
type ErrProtectedBranch struct {
	Branch string
}

func (e ErrProtectedBranch) Error() string {
	return fmt.Sprintf("refusing to merge into protected branch %q", e.Branch)
}
