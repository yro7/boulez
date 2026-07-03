package git

import (
	"claude-squad/cmd"
	"claude-squad/config"
	"claude-squad/session/fs"
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
	// fs manipulates worktree paths on the filesystem. Injected so v2 can
	// swap in a remote FS; defaults to the local FS in the public constructors.
	fs fs.FS
	// worktreeDir is the directory under which this worktree lives. Owned by
	// the Host (LocalHost: ~/.cs2/worktrees; SSHHost: ~/.cs2/worktrees literal,
	// expanded by the remote shell). Stored on the struct so Setup's MkdirAll
	// targets the right host's dir without re-deriving it from the local
	// config (which would be wrong for a remote worktree).
	worktreeDir string
}

// newGitWorktree is the internal constructor that takes every dependency
// explicitly. The public constructors delegate here, defaulting cmdExec/fs
// and computing worktreeDir locally.
func newGitWorktree(repoPath, worktreePath, sessionName, branchName, baseCommitSHA string, isExistingBranch bool, cmdExec cmd.Executor, fsys fs.FS, worktreeDir string) *GitWorktree {
	return &GitWorktree{
		repoPath:         repoPath,
		worktreePath:     worktreePath,
		sessionName:      sessionName,
		branchName:       branchName,
		baseCommitSHA:    baseCommitSHA,
		isExistingBranch: isExistingBranch,
		cmdExec:          cmdExec,
		fs:               fsys,
		worktreeDir:      worktreeDir,
	}
}

func NewGitWorktreeFromStorage(repoPath string, worktreePath string, sessionName string, branchName string, baseCommitSHA string, isExistingBranch bool) *GitWorktree {
	worktreeDir, _ := getWorktreeDirectory()
	return newGitWorktree(repoPath, worktreePath, sessionName, branchName, baseCommitSHA, isExistingBranch, cmd.MakeExecutor(), fs.LocalFS{}, worktreeDir)
}

// NewGitWorktreeFromStorageWithDeps is the WithDeps variant for tests/v2.
// worktreeDir is the Host's worktree directory (not re-derived from local
// config), so a restored remote worktree's Setup targets the right host.
func NewGitWorktreeFromStorageWithDeps(repoPath string, worktreePath string, sessionName string, branchName string, baseCommitSHA string, isExistingBranch bool, cmdExec cmd.Executor, fsys fs.FS, worktreeDir string) *GitWorktree {
	return newGitWorktree(repoPath, worktreePath, sessionName, branchName, baseCommitSHA, isExistingBranch, cmdExec, fsys, worktreeDir)
}

// resolveWorktreePaths resolves the repo root and generates a unique worktree
// path under the given worktreeDir. worktreeDir is provided by the Host
// (LocalHost: local ~/.cs2/worktrees; SSHHost: ~-relative literal), so this
// function is transport-agnostic — it never reads the local config.
func resolveWorktreePaths(repoPath string, branchName string, cmdExec cmd.Executor, worktreeDir string) (resolvedRepo string, worktreePath string, err error) {
	// repoPath is already transport-resolved by the caller (Host.ResolveRepoPath):
	// absolute for LocalHost, passthrough for SSHHost. Do NOT call filepath.Abs
	// here — that resolves against the local cwd, which corrupts a remote
	// relative path into a local-machine path (the "not a git repository" bug).
	// git -C accepts the path as-is on both transports; Root() below returns
	// git's absolute toplevel regardless.
	resolvedRepo, err = NewRepoWithDeps(repoPath, cmdExec).Root()
	if err != nil {
		return "", "", err
	}

	worktreePath = filepath.Join(worktreeDir, sanitizeBranchName(branchName))
	worktreePath = worktreePath + "_" + fmt.Sprintf("%x", time.Now().UnixNano())

	return resolvedRepo, worktreePath, nil
}

// NewGitWorktree creates a new GitWorktree instance
func NewGitWorktree(repoPath string, sessionName string) (tree *GitWorktree, branchname string, err error) {
	worktreeDir, err := getWorktreeDirectory()
	if err != nil {
		return nil, "", err
	}
	return NewGitWorktreeWithDeps(repoPath, sessionName, cmd.MakeExecutor(), fs.LocalFS{}, worktreeDir)
}

// NewGitWorktreeWithDeps is the WithDeps variant for tests/v2. worktreeDir is
// the Host's worktree directory.
func NewGitWorktreeWithDeps(repoPath string, sessionName string, cmdExec cmd.Executor, fsys fs.FS, worktreeDir string) (tree *GitWorktree, branchname string, err error) {
	cfg := config.LoadConfig()
	branchName := fmt.Sprintf("%s%s", cfg.BranchPrefix, sessionName)
	// Sanitize the final branch name to handle invalid characters from any source
	// (e.g., backslashes from Windows domain usernames like DOMAIN\user)
	branchName = sanitizeBranchName(branchName)
	// PII: branchName derives from sessionName (the instance Title) only — the
	// host alias is never an input — so a remote host never appears in branch
	// names (PLAN-ssh-v2.md decision 5).

	repoPath, worktreePath, err := resolveWorktreePaths(repoPath, branchName, cmdExec, worktreeDir)
	if err != nil {
		return nil, "", err
	}

	return newGitWorktree(repoPath, worktreePath, sessionName, branchName, "", false, cmdExec, fsys, worktreeDir), branchName, nil
}

// NewGitWorktreeFromBranch creates a new GitWorktree that uses an existing branch.
// The branch will not be deleted on cleanup.
func NewGitWorktreeFromBranch(repoPath string, branchName string, sessionName string) (*GitWorktree, error) {
	worktreeDir, err := getWorktreeDirectory()
	if err != nil {
		return nil, err
	}
	return NewGitWorktreeFromBranchWithDeps(repoPath, branchName, sessionName, cmd.MakeExecutor(), fs.LocalFS{}, worktreeDir)
}

// NewGitWorktreeFromBranchWithDeps is the WithDeps variant for tests/v2.
func NewGitWorktreeFromBranchWithDeps(repoPath string, branchName string, sessionName string, cmdExec cmd.Executor, fsys fs.FS, worktreeDir string) (*GitWorktree, error) {
	repoPath, worktreePath, err := resolveWorktreePaths(repoPath, branchName, cmdExec, worktreeDir)
	if err != nil {
		return nil, err
	}

	return newGitWorktree(repoPath, worktreePath, sessionName, branchName, "", true, cmdExec, fsys, worktreeDir), nil
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
