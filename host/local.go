package host

import (
	"claude-squad/cmd"
	"claude-squad/config"
	"claude-squad/session/fs"
	"path/filepath"
)

// LocalHost is the Host that runs everything on the machine executing cs2.
// It is today's behaviour: Executor calls os/exec, FS calls os.*, PTY is
// local, worktrees live under the local cs2 config dir, and AutoYes follows
// the global config flag.
type LocalHost struct{}

// Local is a convenient singleton for the common case.
var Local = LocalHost{}

// Name implements Host.
func (LocalHost) Name() string { return "local" }

// Executor implements Host: a local command executor.
func (LocalHost) Executor() cmd.Executor { return cmd.MakeExecutor() }

// FS implements Host: the local filesystem.
func (LocalHost) FS() fs.FS { return fs.LocalFS{} }

// PtyFactory implements Host: a local PTY factory (creack/pty).
func (LocalHost) PtyFactory() PtyFactory { return LocalPtyFactory() }

// WorktreeDir implements Host: the local ~/.cs2/worktrees directory.
func (LocalHost) WorktreeDir() (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "worktrees"), nil
}

// ResolveRepoPath implements Host: a local repo path is resolved against the
// process working directory (filepath.Abs). Best-effort — on error the
// original path is returned, matching the prior fallback behaviour; git -C
// still resolves it. This is the local-only branch of transport-specific path
// resolution (the remote branch is SSHHost.ResolveRepoPath, a passthrough).
func (LocalHost) ResolveRepoPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// AutoYesDefault implements Host: new local instances follow the global
// config flag (preserves today's behaviour where `--auto-yes` enables
// auto-yes locally).
func (LocalHost) AutoYesDefault() bool { return config.LoadConfig().AutoYes }
