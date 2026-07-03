package session

import (
	"os/exec"

	"claude-squad/cmd"
	"claude-squad/host"
	"claude-squad/session/fs"
)

// fakeHost is a test double for host.Host that records what buildWorktree
// passes to its transport-specific seam (ResolveRepoPath), so a test can assert
// the instance resolves the repo path through the host rather than via a local
// filepath.Abs. It is transport-agnostic: Executor/FS/PtyFactory return local
// implementations (the instance's Start is not exercised here — only
// ResolveRepoPath is observed).
type fakeHost struct {
	alias          string
	resolvedPath   string
	resolveCalled bool
}

func (f *fakeHost) Name() string             { return f.alias }
func (f *fakeHost) AutoYesDefault() bool     { return false }
func (f *fakeHost) Executor() cmd.Executor   { return cmd.MakeExecutor() }
func (f *fakeHost) FS() fs.FS                { return fs.LocalFS{} }
func (f *fakeHost) PtyFactory() host.PtyFactory {
	return host.LocalPtyFactory()
}
func (f *fakeHost) WorktreeDir() (string, error) { return "/tmp/cs2-fake-wt", nil }

// ResolveRepoPath records the path and returns it unchanged — mirroring
// SSHHost's passthrough so the test can assert the relative path reaches the
// host (not absolutized by NewInstance or the git layer).
func (f *fakeHost) ResolveRepoPath(path string) string {
	f.resolveCalled = true
	f.resolvedPath = path
	return path
}

// Compile-time guarantee that fakeHost satisfies Host.
var _ host.Host = (*fakeHost)(nil)

// keep os/exec referenced so the import is not dropped if future edits remove
// the only other use in this file's companion tests.
var _ = exec.Command
