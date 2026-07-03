package host

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"claude-squad/cmd"
	"claude-squad/session/fs"
)

// SSHHost runs an instance's environment on a remote machine via the system
// ssh binary. It relies on the user's ssh config (~/.ssh/config), agent, and
// keys — cs2 never stores credentials. Every command, filesystem operation,
// and PTY is routed over `ssh <alias> ...`.
//
// The alias is an entry the user has configured in their ssh config / known
// hosts (e.g. "dev-machine", "gpu-box"). cs2 treats it as opaque; resolving
// it to a host/user/port is ssh's job.
type SSHHost struct {
	alias string
}

// Compile-time guarantee that SSHHost satisfies Host.
var _ Host = SSHHost{}

// NewSSHHost returns an SSHHost bound to the given ssh alias.
func NewSSHHost(alias string) SSHHost {
	return SSHHost{alias: alias}
}

// Alias returns the ssh alias this host connects to.
func (h SSHHost) Alias() string { return h.alias }

// Name implements Host: the ssh alias. Used for InstanceData persistence and
// TUI display only — never in commit messages, branch names, or tmux session
// names (PII discipline, PLAN-ssh-v2.md decision 5).
func (h SSHHost) Name() string { return h.alias }

// AutoYesDefault implements Host: false. AutoYes is off by default on remote
// hosts — auto-approving agent actions on a shared/prod box is riskier than
// locally (decision 3). The user can still toggle it on per-instance.
func (h SSHHost) AutoYesDefault() bool { return false }

// Executor implements Host: an executor that prefixes `ssh <alias>` to every
// command.
func (h SSHHost) Executor() cmd.Executor { return sshExecutor{alias: h.alias} }

// FS implements Host: a filesystem routed over ssh. Paths are ~-relative so
// the remote shell expands them (no $HOME resolution round-trip).
func (h SSHHost) FS() fs.FS { return sshFS{alias: h.alias} }

// WorktreeDir implements Host: the literal ~/.cs2/worktrees, expanded by the
// remote shell when used in an `ssh host git -C <dir> ...` command.
func (h SSHHost) WorktreeDir() (string, error) { return "~/.cs2/worktrees", nil }

// PtyFactory implements Host: a PTY factory that runs `ssh -t <alias> ...`
// under a local PTY (creack/pty). The -t forces a remote TTY so tmux attach
// is interactive. Used by TmuxSession.Attach/Restore for remote sessions.
func (h SSHHost) PtyFactory() PtyFactory { return sshPtyFactory{alias: h.alias} }

// --- Executor ---

// sshBin is the binary name used to connect. Constant so tests can assert
// against it without hardcoding "ssh" in two places.
const sshBin = "ssh"

// sshExecutor wraps every command in `ssh <alias> <cmd...>`. Because ssh joins
// argv with spaces and re-parses via the remote shell, each original arg is
// shell-quoted to survive the round-trip (a path with a space stays one arg).
type sshExecutor struct {
	alias string
}

func (e sshExecutor) Run(c *exec.Cmd) error {
	return exec.Command(sshBin, e.wrap(c.Args)...).Run()
}

func (e sshExecutor) Output(c *exec.Cmd) ([]byte, error) {
	return exec.Command(sshBin, e.wrap(c.Args)...).Output()
}

func (e sshExecutor) CombinedOutput(c *exec.Cmd) ([]byte, error) {
	return exec.Command(sshBin, e.wrap(c.Args)...).CombinedOutput()
}

// wrap returns the argv to run: `ssh <alias> <shell-joined-and-quoted args>`.
// Extracted so tests can assert the wrapping without launching ssh.
func (e sshExecutor) wrap(origArgs []string) []string {
	return []string{sshBin, e.alias, joinShellQuoted(origArgs)}
}

// joinShellQuoted returns the args as a single shell string with each arg
// individually quoted, suitable for passing as one argument to `ssh host
// <string>` (ssh sends <string> to the remote shell).
func joinShellQuoted(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// shellQuote wraps s in single quotes for safe shell consumption, escaping any
// embedded single quotes. This is the standard POSIX-safe quoting: the result
// is interpreted as a literal by a POSIX shell, with no expansions. Handles
// paths with spaces, quotes, backticks, and $ metacharacters.
func shellQuote(s string) string {
	// Replace every ' with '\'' (close quote, escaped quote, reopen quote).
	// Then wrap the whole thing in single quotes.
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// --- FS ---

// sshFS implements fs.FS by running shell commands over ssh. Paths are passed
// to the remote shell, so ~ is expanded remotely. Each operation is a single
// `ssh host sh -c '...'` invocation.
type sshFS struct {
	alias string
}

func (f sshFS) Stat(name string) (os.FileInfo, error) {
	// Emit exists/missing/dir so callers get existence + IsDir without a
	// second round-trip. os.IsNotExist is checked by IsValidWorktree/Cleanup.
	out, err := exec.Command("ssh", f.alias,
		fmt.Sprintf("if [ -d %s ]; then echo dir; elif [ -e %s ]; then echo file; else echo missing; fi", shellQuote(name), shellQuote(name)),
	).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ssh stat %s: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	switch strings.TrimSpace(string(out)) {
	case "dir":
		return minimalFileInfo{name: name, isDir: true}, nil
	case "file":
		return minimalFileInfo{name: name, isDir: false}, nil
	default:
		return nil, errNotExist(name)
	}
}

func (f sshFS) RemoveAll(path string) error {
	out, err := exec.Command("ssh", f.alias,
		fmt.Sprintf("rm -rf -- %s", shellQuote(path)),
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh rm %s: %w (%s)", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (f sshFS) MkdirAll(path string, perm os.FileMode) error {
	out, err := exec.Command("ssh", f.alias,
		fmt.Sprintf("mkdir -p -- %s", shellQuote(path)),
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh mkdir %s: %w (%s)", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (f sshFS) ReadDir(name string) ([]os.DirEntry, error) {
	// Emit one path per line, null-delimited to survive spaces.
	cmd := exec.Command("ssh", f.alias,
		fmt.Sprintf("find %s -mindepth 1 -maxdepth 1 -print0", shellQuote(name)))
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ssh readdir %s: %w", name, err)
	}
	var entries []os.DirEntry
	for _, p := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if p == "" {
			continue
		}
		entries = append(entries, dirEntry{name: p})
	}
	return entries, nil
}
