// Package host bundles the three execution seams a cs2 instance needs
// (command Executor, filesystem FS, PTY factory) behind a single interface,
// plus host-level metadata (name, worktree directory, AutoYes default).
//
// Today cs2 runs everything locally: the Executor calls os/exec directly, the
// FS calls os.* directly, and the PTY is a local pseudo-terminal. For an
// instance whose environment lives on a remote machine, all three must act on
// that remote host instead — silently doing them locally would be a bug, not
// a network error. Host is the seam: LocalHost is today's behaviour; SSHHost
// (v2) wraps the same operations over `ssh host ...`.
//
// Keeping Executor/FS/PtyFactory bundled on one type means the transport
// choice lives in exactly one place. Callers (Instance) depend on Host, not
// on three separate injections, so swapping local for ssh is a single field.
package host

import (
	"claude-squad/cmd"
	"claude-squad/session/fs"
)

// Host is the execution environment of an instance: how to run commands,
// touch the filesystem, allocate a PTY, and where worktrees live. One
// implementation = one transport. LocalHost is the default; SSHHost (v2) is
// the remote transport.
type Host interface {
	// Name is the human/engineering identifier of the host: "local" for the
	// machine running cs2, or an ssh alias like "dev-machine". Used for
	// InstanceData persistence and TUI display. Never appears in commit
	// messages, branch names, or tmux session names (PII discipline).
	Name() string

	// Executor runs commands (git, tmux, gh). LocalHost returns a local
	// executor; SSHHost returns one that prefixes `ssh <alias>`.
	Executor() cmd.Executor

	// FS manipulates the filesystem. LocalHost delegates to os.*; SSHHost
	// routes over ssh.
	FS() fs.FS

	// PtyFactory allocates PTYs for tmux attach/restore. LocalHost uses
	// creack/pty directly; SSHHost starts `ssh -t <alias> ...` under a PTY.
	PtyFactory() PtyFactory

	// WorktreeDir is the directory under which cs2 worktrees for this host
	// are created. LocalHost returns an absolute local path; SSHHost returns
	// a ~-relative literal expanded by the remote shell (no $HOME resolution
	// round-trip).
	WorktreeDir() (string, error)

	// AutoYesDefault is whether new instances on this host start with
	// AutoYes enabled. LocalHost follows the global config flag; SSHHost
	// returns false (AutoYes is off by default on remote hosts — decision 3
	// of PLAN-ssh-v2.md).
	AutoYesDefault() bool
}
