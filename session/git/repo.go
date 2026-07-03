package git

import (
	"claude-squad/cmd"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// MaxBranchSearchResults is the maximum number of branches returned by Repo.SearchBranches.
const MaxBranchSearchResults = 50

// Repo wraps a repository path with an injectable command executor. It owns
// repo-level operations (branches, fetch, root resolution) that have no
// dependency on a cs2 worktree. Adding SSH = swap the Executor; Repo itself
// is transport-agnostic.
//
// Cohesion: these operations all act on a repository identified by a path,
// independent of any worktree cs2 may create inside it. Callers that need
// worktree-specific state (diff, commit, dirty check) use GitWorktree instead.
type Repo struct {
	path    string
	cmdExec cmd.Executor
}

// NewRepo returns a Repo backed by the default local command executor.
func NewRepo(path string) *Repo {
	return NewRepoWithDeps(path, cmd.MakeExecutor())
}

// NewRepoWithDeps returns a Repo with an explicit command executor, for
// testing and for routing commands over a non-local transport (SSH in v2).
func NewRepoWithDeps(path string, cmdExec cmd.Executor) *Repo {
	return &Repo{path: path, cmdExec: cmdExec}
}

// Path returns the repository path this Repo operates on.
func (r *Repo) Path() string {
	return r.path
}

// FetchBranches fetches and prunes remote-tracking branches (best-effort,
// won't fail if offline).
func (r *Repo) FetchBranches() {
	c := exec.Command("git", "-C", r.path, "fetch", "--prune")
	_ = r.cmdExec.Run(c)
}

// SearchBranches searches for branches whose name contains filter
// (case-insensitive), ordered by most recently updated first. Returns at most
// MaxBranchSearchResults. If filter is empty, returns all branches up to the
// limit. Remote-tracking branches are deduplicated against their local
// counterparts (the "origin/" prefix is stripped).
func (r *Repo) SearchBranches(filter string) ([]string, error) {
	c := exec.Command("git", "-C", r.path, "branch", "-a",
		"--sort=-committerdate",
		"--format=%(refname:short)")
	output, err := r.cmdExec.CombinedOutput(c)
	if err != nil {
		return nil, fmt.Errorf("failed to list branches: %s (%w)", output, err)
	}

	seen := make(map[string]bool)
	var branches []string
	lower := strings.ToLower(filter)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "HEAD") {
			continue
		}
		name := strings.TrimPrefix(line, "origin/")
		if seen[name] {
			continue
		}
		seen[name] = true
		if filter != "" && !strings.Contains(strings.ToLower(name), lower) {
			continue
		}
		branches = append(branches, name)
		if len(branches) >= MaxBranchSearchResults {
			break
		}
	}
	return branches, nil
}

// Root resolves the top-level directory of the repository containing the
// Repo's path.
func (r *Repo) Root() (string, error) {
	c := exec.Command("git", "-C", r.path, "rev-parse", "--show-toplevel")
	out, err := r.cmdExec.Output(c)
	if err != nil {
		return "", fmt.Errorf("failed to find Git repository root from path: %s", r.path)
	}
	return strings.TrimSpace(string(out)), nil
}

// IsGitRepo reports whether the path is within a git repository.
func (r *Repo) IsGitRepo() bool {
	c := exec.Command("git", "-C", r.path, "rev-parse", "--show-toplevel")
	return r.cmdExec.Run(c) == nil
}

// FilterExistingRepos returns the subset of paths that are git repositories
// accessible via exec. Each path is checked with `git -C <path> rev-parse`, so
// a remote host's executor (SSH) checks the path on that host: a repo that
// exists locally but not on the target host is filtered out. Paths are checked
// concurrently, which matters for a remote host without SSH multiplexing
// (each check is a separate `ssh host ...` round-trip).
//
// Order is preserved (stable). A nil/empty executor returns paths unchanged
// (caller is responsible for providing a real executor). Used by the repo
// selector to show only repos that actually exist on the chosen host.
func FilterExistingRepos(paths []string, exec cmd.Executor) []string {
	if exec == nil || len(paths) == 0 {
		return paths
	}
	type result struct {
		index int
		ok    bool
	}
	results := make([]result, len(paths))
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			results[i] = result{index: i, ok: NewRepoWithDeps(p, exec).IsGitRepo()}
		}(i, p)
	}
	wg.Wait()
	var kept []string
	for _, r := range results {
		if r.ok {
			kept = append(kept, paths[r.index])
		}
	}
	return kept
}
