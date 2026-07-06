package host

import (
	"os"
	"os/exec"
	"strings"

	"github.com/creack/pty"
)

// PtyFactory starts a process attached to a pseudo-terminal (PTY) and returns
// the PTY's file handle. It is the PTY analogue of cmd.Executor: LocalPty
// uses creack/pty directly; an SSH variant (v2) starts `ssh -t <alias> ...`
// under a local PTY so the remote tmux attach is interactive.
//
// Moved here from session/tmux: a PTY starter is not tmux-specific, and
// bundling it on Host keeps the transport choice in one place. tmux imports
// host.PtyFactory; this package must not import tmux (would cycle).
type PtyFactory interface {
	Start(cmd *exec.Cmd) (*os.File, error)
	Close()
}

// LocalPty starts a real pseudo-terminal using creack/pty.
type LocalPty struct{}

// Start implements PtyFactory.
func (LocalPty) Start(cmd *exec.Cmd) (*os.File, error) { return pty.Start(cmd) }

// Close implements PtyFactory.
func (LocalPty) Close() {}

// LocalPtyFactory returns the default local PTY factory.
func LocalPtyFactory() PtyFactory { return LocalPty{} }

// sshPtyFactory starts a process under a local PTY (creack/pty) with the
// command wrapped in `ssh [<-t>] <alias> ...`. The local PTY is always
// allocated (creack/pty needs a master/slave pair to drive the process),
// but the REMOTE PTY (-t) is requested ONLY for interactive commands (tmux
// attach). For non-interactive commands (tmux new-session -d) -t must be
// OMITTED: a remote PTY, once closed by ssh on disconnect, sends SIGHUP to
// the tmux server's session and the server dies — so `tmux new-session -d`
// appeared to succeed but `has-session` then reported "no server running".
// Without -t, ssh runs the detached command and exits without disturbing
// the daemonized tmux server. This is the PTY analogue of sshExecutor, but
// interactive vs non-interactive is decided per-command (attach needs a
// remote TTY; new-session -d must not have one).
type sshPtyFactory struct {
	alias string
}

func (f sshPtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	return pty.Start(f.command(cmd))
}

// command builds the *exec.Cmd that runs cmd's argv over ssh under a local
// PTY. -t is included only when the command is interactive (tmux attach*);
// otherwise omitted so a remote PTY closure can't kill a detached tmux
// server. Extracted so tests can assert the wrapping (alias, conditional -t,
// shell-joined args) without launching ssh or allocating a PTY. Built
// directly as `exec.Command("ssh", ...) ` — never re-prepend sshBin (the
// double-"ssh" bug class).
func (f sshPtyFactory) command(cmd *exec.Cmd) *exec.Cmd {
	args := []string{"ssh"}
	if wantsRemoteTTY(cmd.Args) {
		args = append(args, "-t")
	}
	args = append(args, f.alias, joinShellQuoted(cmd.Args))
	return exec.Command(args[0], args[1:]...)
}

// wantsRemoteTTY reports whether the command needs a REMOTE PTY allocated by
// ssh (-t). Only interactive tmux attach does: the user's terminal must be
// wired to the remote tmux session. Everything else routed through the PTY
// factory today (tmux new-session -d) is fire-and-forget and must NOT have a
// remote PTY — its closure on ssh disconnect kills the daemonized tmux
// server (SIGHUP), so the just-created session vanishes and has-session
// reports "no server running". Decided by command inspection (not a flag on
// Start) so the PtyFactory interface stays one method.
func wantsRemoteTTY(argv []string) bool {
	// argv[0] is the binary (e.g. "tmux"). argv[1] is the subcommand. We
	// request -t only for attach subcommands; new-session/kill/etc. don't want
	// a remote PTY.
	if len(argv) < 2 {
		return false
	}
	sub := strings.ToLower(argv[1])
	return strings.HasPrefix(sub, "attach")
}

func (f sshPtyFactory) Close() {}
