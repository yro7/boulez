package host

import (
	"os"
	"os/exec"

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

// sshPtyFactory starts a process under a local PTY (creack/pty) but with the
// command wrapped in `ssh -t [control opts] <alias> ...`. The -t forces PTY
// allocation on the remote so interactive tmux attach/restore works. The
// control opts (-o ControlPath=<socket>) make the attach ride the
// ControlMaster when one is up; when socket is "" they are omitted (plain
// one-shot ssh). This is the PTY analogue of sshExecutor: same wrapping, but
// the local PTY makes the ssh session interactive.
type sshPtyFactory struct {
	alias  string
	socket string // control socket; "" => plain one-shot ssh (no muxing)
}

func (f sshPtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	return pty.Start(f.command(cmd))
}

// command builds the *exec.Cmd that runs cmd's argv over
// `ssh -t [control opts] <alias> ...` under a local PTY. Delegates to
// sshInteractiveArgs so the argv assembly lives in one tested place (also
// used by SSHHost.AttachCmd). Extracted so tests can assert the wrapping
// (alias, -t, control opts, shell-joined args) without launching ssh or
// allocating a PTY. Built directly as `exec.Command("ssh", ...)` — never
// re-prepend sshBin (the double-"ssh" bug class).
func (f sshPtyFactory) command(cmd *exec.Cmd) *exec.Cmd {
	args := sshInteractiveArgs(f.alias, f.socket, cmd.Args)
	return exec.Command(args[0], args[1:]...)
}

func (f sshPtyFactory) Close() {}

// sshInteractiveArgs returns the full argv to run a command interactively
// over ssh with a remote PTY: `ssh -t [control opts] <alias> <shell-joined
// args>`. The -t forces PTY allocation on the remote so interactive commands
// (tmux attach-session) work; the control opts (-o ControlPath=<socket>) ride
// the ControlMaster when one is up (omitted when socket is ""). Extracted so
// AttachCmd and sshPtyFactory.command share one argv builder; tested without
// launching ssh.
func sshInteractiveArgs(alias, socket string, cmdArgs []string) []string {
	args := []string{sshBin, "-t"}
	args = append(args, sshControlArgs(socket)...)
	args = append(args, alias, joinShellQuoted(cmdArgs))
	return args
}
