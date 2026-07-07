// Package host bundles the three execution seams a boulez instance needs
// (command Executor, filesystem FS, PTY factory) behind a single interface,
// plus host-level metadata (name, worktree directory, AutoYes default).
//
// Today boulez runs everything locally: the Executor calls os/exec directly, the
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
	"os/exec"

	"github.com/yro7/boulez/cmd"
	"github.com/yro7/boulez/session/fs"
)

// Host is the execution environment of an instance: how to run commands,
// touch the filesystem, allocate a PTY, and where worktrees live. One
// implementation = one transport. LocalHost is the default; SSHHost (v2) is
// the remote transport.
type Host interface {
	// Name is the human/engineering identifier of the host: "local" for the
	// machine running boulez, or an ssh alias like "dev-machine". Used for
	// InstanceData persistence and TUI display. Never appears in commit
	// messages, branch names, or tmux session names (PII discipline).
	Name() string

	// Executor runs commands (git, tmux, gh). LocalHost returns a local
	// executor; SSHHost returns one that prefixes `ssh <alias>`.
	Executor() cmd.Executor

	// FS manipulates the filesystem. LocalHost delegates to os.*; SSHHost
	// routes over ssh.
	FS() fs.FS

	// ResolveRepoPath normalizes a user-supplied repo path for this host's
	// transport. LocalHost resolves it against the process working directory
	// (filepath.Abs, best-effort) so a stored path survives a cwd change;
	// SSHHost returns it unchanged so the remote shell resolves relative
	// paths (and ~) against the remote $HOME — resolving locally would point
	// at a path on the wrong machine. Called once at Start, after the host is
	// known, before the worktree is built.
	ResolveRepoPath(path string) string

	// WorktreeDir is the directory under which boulez worktrees for this host
	// are created. LocalHost returns an absolute local path; SSHHost returns
	// an absolute remote path (<remote-$HOME>/.boulez/worktrees), resolving
	// $HOME on the remote at Start. The path is absolute (not ~-relative)
	// because it flows through single-quoted argv (joinShellQuoted) and
	// `git -C`, neither of which expands ~.
	WorktreeDir() (string, error)

	// EnsureConnected establishes any long-lived transport connection needed
	// before the instance issues commands. For SSHHost this starts (or verifies)
	// a ControlMaster so the burst of git/tmux commands and the daemon's
	// per-second poll loop multiplex over one connection instead of opening
	// one-shot `ssh` connections (which re-attempt LocalForwards every time and
	// race short tmux poll timeouts). For LocalHost it is a no-op. Best-effort:
	// a failure is logged and boulez falls back to one-shot transport; it never
	// aborts Start.
	EnsureConnected() error

	// AttachCmd returns the *exec.Cmd that interactively attaches the user's
	// terminal to a named tmux session on this host. The TUI runs it via
	// tea.ExecProcess, which releases the Bubbletea terminal (alt-screen, raw
	// mode, mouse) for the command's duration and restores it on exit. LocalHost
	// returns `tmux attach-session -t <name>`; SSHHost returns
	// `ssh -t [control opts] <alias> tmux attach-session -t <name>` — the -t
	// forces a remote PTY so the attach is interactive, and the control opts
	// ride the ControlMaster when one is up. The returned command is run on the
	// real terminal; boulez does not allocate its own PTY for it (the local
	// terminal is already a tty, and ssh -t allocates the remote one).
	AttachCmd(sessionName string) *exec.Cmd

	// AutoYesDefault is whether new instances on this host start with
	// AutoYes enabled. LocalHost follows the global config flag; SSHHost
	// returns false (AutoYes is off by default on remote hosts).
	AutoYesDefault() bool
}
