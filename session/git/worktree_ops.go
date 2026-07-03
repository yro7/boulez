package git

import (
	"claude-squad/cmd"
	"claude-squad/log"
	"claude-squad/session/fs"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Setup creates a new worktree for the session
func (g *GitWorktree) Setup() error {
	// Ensure worktrees directory exists early (can be done in parallel with branch check).
	// g.worktreeDir is the Host's dir (local or ~-relative for ssh), so a remote
	// worktree's mkdir targets the right host via g.fs.MkdirAll.
	if err := g.fs.MkdirAll(g.worktreeDir, 0755); err != nil {
		return err
	}

	// If this worktree uses a pre-existing branch, always set up from that branch
	// (it may exist locally or only on the remote).
	if g.isExistingBranch {
		return g.setupFromExistingBranch()
	}

	// Check if branch exists using git CLI (much faster than go-git PlainOpen)
	_, err := g.runGitCommand(g.repoPath, "show-ref", "--verify", fmt.Sprintf("refs/heads/%s", g.branchName))
	if err == nil {
		return g.setupFromExistingBranch()
	}
	return g.setupNewWorktree()
}

// setupFromExistingBranch creates a worktree from an existing branch
func (g *GitWorktree) setupFromExistingBranch() error {
	// Directory already created in Setup(), skip duplicate creation

	// Clean up any existing worktree first
	_, _ = g.runGitCommand(g.repoPath, "worktree", "remove", "-f", g.worktreePath) // Ignore error if worktree doesn't exist
	// If the directory is still there (orphaned, not registered with git), drop it so `git worktree add` won't fail.
	_ = g.fs.RemoveAll(g.worktreePath)

	// Check if the local branch exists
	_, localErr := g.runGitCommand(g.repoPath, "show-ref", "--verify", fmt.Sprintf("refs/heads/%s", g.branchName))
	if localErr != nil {
		// Local branch doesn't exist — check if remote tracking branch exists
		_, remoteErr := g.runGitCommand(g.repoPath, "show-ref", "--verify", fmt.Sprintf("refs/remotes/origin/%s", g.branchName))
		if remoteErr != nil {
			return fmt.Errorf("branch %s not found locally or on remote", g.branchName)
		}
		// Create a local tracking branch via worktree add -b
		if _, err := g.runGitCommand(g.repoPath, "worktree", "add", "-b", g.branchName, g.worktreePath, fmt.Sprintf("origin/%s", g.branchName)); err != nil {
			return fmt.Errorf("failed to create worktree from remote branch %s: %w", g.branchName, err)
		}
		return nil
	}

	// Create a new worktree from the existing local branch
	if _, err := g.runGitCommand(g.repoPath, "worktree", "add", g.worktreePath, g.branchName); err != nil {
		return fmt.Errorf("failed to create worktree from branch %s: %w", g.branchName, err)
	}

	return nil
}

// setupNewWorktree creates a new worktree from HEAD
func (g *GitWorktree) setupNewWorktree() error {
	// Clean up any existing worktree first
	_, _ = g.runGitCommand(g.repoPath, "worktree", "remove", "-f", g.worktreePath) // Ignore error if worktree doesn't exist
	// If the directory is still there (orphaned, not registered with git), drop it so `git worktree add` won't fail.
	_ = g.fs.RemoveAll(g.worktreePath)

	// Clean up any existing branch using git CLI (much faster than go-git PlainOpen)
	_, _ = g.runGitCommand(g.repoPath, "branch", "-D", g.branchName) // Ignore error if branch doesn't exist

	output, err := g.runGitCommand(g.repoPath, "rev-parse", "HEAD")
	if err != nil {
		if strings.Contains(err.Error(), "fatal: ambiguous argument 'HEAD'") ||
			strings.Contains(err.Error(), "fatal: not a valid object name") ||
			strings.Contains(err.Error(), "fatal: HEAD: not a valid object name") {
			return fmt.Errorf("this appears to be a brand new repository: please create an initial commit before creating an instance")
		}
		return fmt.Errorf("failed to get HEAD commit hash: %w", err)
	}
	headCommit := strings.TrimSpace(string(output))
	g.baseCommitSHA = headCommit

	// Create a new worktree from the HEAD commit
	// Otherwise, we'll inherit uncommitted changes from the previous worktree.
	// This way, we can start the worktree with a clean slate.
	// TODO: we might want to give an option to use main/master instead of the current branch.
	if _, err := g.runGitCommand(g.repoPath, "worktree", "add", "-b", g.branchName, g.worktreePath, headCommit); err != nil {
		return fmt.Errorf("failed to create worktree from commit %s: %w", headCommit, err)
	}

	return nil
}

// Cleanup removes the worktree and associated branch
func (g *GitWorktree) Cleanup() error {
	var errs []error

	// Check if worktree path exists before attempting removal
	if _, err := g.fs.Stat(g.worktreePath); err == nil {
		// Remove the worktree using git command
		if _, err := g.runGitCommand(g.repoPath, "worktree", "remove", "-f", g.worktreePath); err != nil {
			errs = append(errs, err)
		}
	} else if !os.IsNotExist(err) {
		// Only append error if it's not a "not exists" error
		errs = append(errs, fmt.Errorf("failed to check worktree path: %w", err))
	}

	// Delete the branch using git CLI, but skip if this is a pre-existing branch
	if !g.isExistingBranch {
		if _, err := g.runGitCommand(g.repoPath, "branch", "-D", g.branchName); err != nil {
			// Only log if it's not a "branch not found" error
			if !strings.Contains(err.Error(), "not found") {
				errs = append(errs, fmt.Errorf("failed to remove branch %s: %w", g.branchName, err))
			}
		}
	}

	// Prune the worktree to clean up any remaining references
	if err := g.Prune(); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return g.combineErrors(errs)
	}

	return nil
}

// Remove removes the worktree but keeps the branch
func (g *GitWorktree) Remove() error {
	// Remove the worktree using git command
	if _, err := g.runGitCommand(g.repoPath, "worktree", "remove", "-f", g.worktreePath); err != nil {
		return fmt.Errorf("failed to remove worktree: %w", err)
	}

	return nil
}

// Prune removes all working tree administrative files and directories
func (g *GitWorktree) Prune() error {
	if _, err := g.runGitCommand(g.repoPath, "worktree", "prune"); err != nil {
		return fmt.Errorf("failed to prune worktrees: %w", err)
	}
	return nil
}

// CleanupWorktrees removes all worktrees under the local worktrees directory
// and their associated branches. It is the "nuke" path used by `cs2 reset`.
// Defaults to local deps; CleanupWorktreesWithDeps is the testable / host-aware
// variant. Remote worktrees are not swept here (they live on the remote host,
// not the local worktrees dir) — they are cleaned per-instance via
// GitWorktree.Cleanup, which routes through the instance's host.
func CleanupWorktrees() error {
	worktreeDir, err := getWorktreeDirectory()
	if err != nil {
		return fmt.Errorf("failed to get worktree directory: %w", err)
	}
	return CleanupWorktreesWithDeps(cmd.MakeExecutor(), fs.LocalFS{}, worktreeDir)
}

// CleanupWorktreesWithDeps is the host+repo-aware sweep. It reads worktreeDir
// via fsys and, for each worktree, removes it and its branch from that
// worktree's OWN repository (via cmdExec with `git -C <repo-root> ...`) — not
// from whatever repo happens to be in the cwd.
//
// This closes dette #1: the previous implementation ran `git worktree list`
// and `git branch -D` without `-C`, operating on the cwd's repo — a latent
// multi-repo bug (a sweep from repo A's cwd would silently fail to delete
// branches belonging to repo B, and would never see A's worktrees if the
// porcelain list didn't cover them). Routing per-worktree through -C fixes it
// and makes the sweep independent of cwd.
//
// worktreeDir is the Host's worktree directory. For LocalHost this is the local
// ~/.cs2/worktrees; for an SSHHost it would be the ~-relative literal — but
// note this sweep only sees entries fsys can list, so in practice it covers
// local worktrees (remote ones are cleaned per-instance through their host).
func CleanupWorktreesWithDeps(cmdExec cmd.Executor, fsys fs.FS, worktreeDir string) error {
	entries, err := fsys.ReadDir(worktreeDir)
	if err != nil {
		return fmt.Errorf("failed to read worktree directory: %w", err)
	}

	// Prune each distinct repo once at the end (a worktree remove leaves no
	// stale admin, but prune is cheap insurance and cleans any orphans).
	prunedRepos := make(map[string]bool)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		worktreePath := filepath.Join(worktreeDir, entry.Name())

		// Resolve the main repo that owns this worktree, from the worktree's
		// own context (git -C <worktree> ...). Empty => not a git worktree or
		// corrupt; fall back to a raw directory removal.
		repoRoot := worktreeRepoRoot(cmdExec, worktreePath)
		// Branch checked out in this worktree (cs2 branches are named after
		// the instance title). Empty => detached or unreadable; skip branch
		// deletion but still drop the directory.
		branch := worktreeBranch(cmdExec, worktreePath)

		if repoRoot != "" {
			// Remove the worktree from its repo's admin. Run from repoRoot (not
			// the worktree dir) so git isn't operating inside the dir it removes.
			if err := cmdExec.Run(exec.Command("git", "-C", repoRoot, "worktree", "remove", "-f", worktreePath)); err != nil {
				// Corrupt/orphaned: git can't remove it via admin; drop the dir
				// directly and let prune clean the stale admin below.
				_ = fsys.RemoveAll(worktreePath)
			}
			// Delete the branch now that the worktree holding it is gone (git
			// refuses to delete a branch checked out in a live worktree).
			if branch != "" {
				if err := cmdExec.Run(exec.Command("git", "-C", repoRoot, "branch", "-D", branch)); err != nil {
					if !strings.Contains(err.Error(), "not found") {
						log.ErrorLog.Printf("failed to delete branch %s: %v", branch, err)
					}
				}
			}
			if !prunedRepos[repoRoot] {
				_ = cmdExec.Run(exec.Command("git", "-C", repoRoot, "worktree", "prune"))
				prunedRepos[repoRoot] = true
			}
		} else {
			// Not a git worktree (or git can't read it): just drop the dir.
			_ = fsys.RemoveAll(worktreePath)
		}

		// Safety: ensure the directory is gone regardless of the path above
		// (idempotent — RemoveAll on a removed dir is a no-op).
		_ = fsys.RemoveAll(worktreePath)
	}

	return nil
}

// worktreeRepoRoot resolves the main repository root that owns the worktree at
// worktreePath, via git's --git-common-dir (the dir shared across all
// worktrees of a repo; its parent is the repo root). Returns "" if the path is
// not a git worktree or git fails, so the caller can fall back to a raw
// RemoveAll.
func worktreeRepoRoot(cmdExec cmd.Executor, worktreePath string) string {
	out, err := cmdExec.Output(exec.Command("git", "-C", worktreePath, "rev-parse", "--git-common-dir"))
	if err != nil {
		return ""
	}
	commonDir := strings.TrimSpace(string(out))
	if commonDir == "" {
		return ""
	}
	if !filepath.IsAbs(commonDir) {
		// --git-common-dir can be relative to the worktree; resolve it.
		commonDir = filepath.Join(worktreePath, commonDir)
	}
	return filepath.Dir(filepath.Clean(commonDir)) // strip /.git -> repo root
}

// worktreeBranch returns the branch checked out in the worktree at worktreePath
// ("" if detached HEAD, unreadable, or not a git worktree).
func worktreeBranch(cmdExec cmd.Executor, worktreePath string) string {
	out, err := cmdExec.Output(exec.Command("git", "-C", worktreePath, "branch", "--show-current"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
