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
