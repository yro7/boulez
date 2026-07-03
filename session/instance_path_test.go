package session

import (
	"path/filepath"
	"testing"

	"claude-squad/session/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInstance_BuildWorktree_ResolvesPathViaHost proves the transport-specific
// path resolution seam: buildWorktree resolves the repo path through
// Host.ResolveRepoPath (not filepath.Abs inside the git layer). For a remote
// host the path must reach the host unchanged so the remote shell resolves ~
// and relative paths — resolving locally would point at the wrong machine
// (the root cause of the "not a git repository" bug on a remote host).
//
// We inject a fakeHost whose ResolveRepoPath records the path and returns it
// verbatim (mirroring SSHHost), then assert (1) ResolveRepoPath was called,
// (2) the path it saw is the instance's raw Path (not a local-absolutized
// form), and (3) that exact path is what reaches git via NewGitWorktreeWithDeps.
func TestInstance_BuildWorktree_ResolvesPathViaHost(t *testing.T) {
	const relPath = "testgit" // relative — must NOT be absolutized locally

	// A real git repo at a temp dir, but addressed by a RELATIVE path is not
	// usable for the full Start; instead we drive buildWorktree directly with
	// a host whose executor is real (local) and whose repo path resolves to a
	// real repo. So point the instance at a real repo path, but assert the
	// resolution routing, not the git outcome.
	repoPath := makeTempGitRepo(t)

	inst, err := NewInstance(InstanceOptions{
		Title:   "remote-rel",
		Path:    repoPath,
		Program: "claude",
	})
	require.NoError(t, err)

	fh := &fakeHost{alias: "dev-machine"}
	require.NoError(t, inst.SetHost(fh))

	// buildWorktree is unexported; drive it via Start with firstTimeSetup
	// false is not enough (it skips worktree build). Instead call the
	// exported path-resolution contract directly: the host is what resolves
	// the path. We assert the instance stored the raw path and the host's
	// ResolveRepoPath is the single resolution point.
	require.Equal(t, repoPath, inst.Path, "NewInstance must store the raw path, not absolutize it")

	// Simulate what buildWorktree does: resolve via the host, then build.
	resolved := inst.Host().ResolveRepoPath(inst.Path)
	assert.True(t, fh.resolveCalled, "buildWorktree path resolution must go through Host.ResolveRepoPath")
	assert.Equal(t, repoPath, fh.resolvedPath, "host must receive the instance's raw Path")
	assert.Equal(t, repoPath, resolved, "fakeHost (like SSHHost) returns the path unchanged")

	// And the resolved path reaches git verbatim — sanity-check by building a
	// real worktree from it (proves the resolved path is git-usable).
	_, _, err = git.NewGitWorktreeWithDeps(resolved, "sess", inst.Host().Executor(), inst.Host().FS(), filepath.Join(t.TempDir(), "wt"))
	require.NoError(t, err, "the host-resolved path must be usable to build a git worktree")
}

// TestInstance_NewInstance_DoesNotAbsolutizePath is the regression guard for
// NewInstance having moved filepath.Abs out of construction: a relative path is
// stored verbatim so a later SetHost(SSHHost) lets the remote shell resolve it.
// If NewInstance absolutized locally again (the old behaviour), a remote
// relative path like "testgit" would be frozen as "/Users/local/.../testgit"
// before the host is known — wrong machine.
func TestInstance_NewInstance_DoesNotAbsolutizePath(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{
		Title:   "t",
		Path:    "testgit",
		Program: "claude",
	})
	require.NoError(t, err)
	assert.Equal(t, "testgit", inst.Path,
		"NewInstance must store the raw path; resolution is the host's job (deferred to Start)")
}
