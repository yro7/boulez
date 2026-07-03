package git

import (
	"claude-squad/cmd"
	"claude-squad/config"
	"claude-squad/log"
	"fmt"
	"path/filepath"
	"time"
)

func getWorktreeDirectory() (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(configDir, "worktrees"), nil
}

// GitWorktree manages git worktree operations for a session
type GitWorktree struct {
	// Path to the repository
	repoPath string
	// Path to the worktree
	worktreePath string
	// Name of the session
	sessionName string
	// Branch name for the worktree
	branchName string
	// Base commit hash for the worktree
	baseCommitSHA string
	// isExistingBranch is true if the branch existed before the session was created.
	// When true, the branch will not be deleted on cleanup.
	isExistingBranch bool
	// cmdExec runs git/gh commands. Injected so v2 can swap in an SSH
	// transport; defaults to the local executor in the public constructors.
	cmdExec cmd.Executor
}

// newGitWorktree is the internal constructor that takes every dependency
// explicitly. The public constructors delegate here, defaulting cmdExec.
func newGitWorktree(repoPath, worktreePath, sessionName, branchName, baseCommitSHA string, isExistingBranch bool, cmdExec cmd.Executor) *GitWorktree {
	return &GitWorktree{
		repoPath:         repoPath,
		worktreePath:     worktreePath,
		sessionName:      sessionName,
		branchName:       branchName,
		baseCommitSHA:    baseCommitSHA,
		isExistingBranch: isExistingBranch,
		cmdExec:          cmdExec,
	}
}

func NewGitWorktreeFromStorage(repoPath string, worktreePath string, sessionName string, branchName string, baseCommitSHA string, isExistingBranch bool) *GitWorktree {
	return newGitWorktree(repoPath, worktreePath, sessionName, branchName, baseCommitSHA, isExistingBranch, cmd.MakeExecutor())
}

// NewGitWorktreeFromStorageWithDeps is the WithDeps variant for tests/v2.
func NewGitWorktreeFromStorageWithDeps(repoPath string, worktreePath string, sessionName string, branchName string, baseCommitSHA string, isExistingBranch bool, cmdExec cmd.Executor) *GitWorktree {
	return newGitWorktree(repoPath, worktreePath, sessionName, branchName, baseCommitSHA, isExistingBranch, cmdExec)
}

// resolveWorktreePaths resolves the repo root and generates a unique worktree path for the given branch name.
func resolveWorktreePaths(repoPath string, branchName string, cmdExec cmd.Executor) (resolvedRepo string, worktreePath string, err error) {
	absPath, err := filepath.Abs(repoPath)
	if err != nil {
		log.ErrorLog.Printf("git worktree path abs error, falling back to repoPath %s: %s", repoPath, err)
		absPath = repoPath
	}

	resolvedRepo, err = NewRepoWithDeps(absPath, cmdExec).Root()
	if err != nil {
		return "", "", err
	}

	worktreeDir, err := getWorktreeDirectory()
	if err != nil {
		return "", "", err
	}

	worktreePath = filepath.Join(worktreeDir, sanitizeBranchName(branchName))
	worktreePath = worktreePath + "_" + fmt.Sprintf("%x", time.Now().UnixNano())

	return resolvedRepo, worktreePath, nil
}

// NewGitWorktree creates a new GitWorktree instance
func NewGitWorktree(repoPath string, sessionName string) (tree *GitWorktree, branchname string, err error) {
	return NewGitWorktreeWithDeps(repoPath, sessionName, cmd.MakeExecutor())
}

// NewGitWorktreeWithDeps is the WithDeps variant for tests/v2.
func NewGitWorktreeWithDeps(repoPath string, sessionName string, cmdExec cmd.Executor) (tree *GitWorktree, branchname string, err error) {
	cfg := config.LoadConfig()
	branchName := fmt.Sprintf("%s%s", cfg.BranchPrefix, sessionName)
	// Sanitize the final branch name to handle invalid characters from any source
	// (e.g., backslashes from Windows domain usernames like DOMAIN\user)
	branchName = sanitizeBranchName(branchName)

	repoPath, worktreePath, err := resolveWorktreePaths(repoPath, branchName, cmdExec)
	if err != nil {
		return nil, "", err
	}

	return newGitWorktree(repoPath, worktreePath, sessionName, branchName, "", false, cmdExec), branchName, nil
}

// NewGitWorktreeFromBranch creates a new GitWorktree that uses an existing branch.
// The branch will not be deleted on cleanup.
func NewGitWorktreeFromBranch(repoPath string, branchName string, sessionName string) (*GitWorktree, error) {
	return NewGitWorktreeFromBranchWithDeps(repoPath, branchName, sessionName, cmd.MakeExecutor())
}

// NewGitWorktreeFromBranchWithDeps is the WithDeps variant for tests/v2.
func NewGitWorktreeFromBranchWithDeps(repoPath string, branchName string, sessionName string, cmdExec cmd.Executor) (*GitWorktree, error) {
	repoPath, worktreePath, err := resolveWorktreePaths(repoPath, branchName, cmdExec)
	if err != nil {
		return nil, err
	}

	return newGitWorktree(repoPath, worktreePath, sessionName, branchName, "", true, cmdExec), nil
}

// IsExistingBranch returns whether this worktree uses a pre-existing branch
func (g *GitWorktree) IsExistingBranch() bool {
	return g.isExistingBranch
}

// GetWorktreePath returns the path to the worktree
func (g *GitWorktree) GetWorktreePath() string {
	return g.worktreePath
}

// GetBranchName returns the name of the branch associated with this worktree
func (g *GitWorktree) GetBranchName() string {
	return g.branchName
}

// GetRepoPath returns the path to the repository
func (g *GitWorktree) GetRepoPath() string {
	return g.repoPath
}

// GetRepoName returns the name of the repository (last part of the repoPath).
func (g *GitWorktree) GetRepoName() string {
	return filepath.Base(g.repoPath)
}

// GetBaseCommitSHA returns the base commit SHA for the worktree
func (g *GitWorktree) GetBaseCommitSHA() string {
	return g.baseCommitSHA
}
