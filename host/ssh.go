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

// ResolveRepoPath implements Host: a remote repo path is returned unchanged so
// the remote shell resolves it. Relative paths and ~ expand against the
// remote $HOME (ssh non-interactive sessions start there, stably across
// invocations); resolving locally with filepath.Abs would produce a path on
// the wrong machine (e.g. /Users/local/.../testgit), which is exactly the bug
// where a remote relative path failed as "not a git repository".
func (h SSHHost) ResolveRepoPath(path string) string { return path }

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
	return e.command(c).Run()
}

func (e sshExecutor) Output(c *exec.Cmd) ([]byte, error) {
	return e.command(c).Output()
}

func (e sshExecutor) CombinedOutput(c *exec.Cmd) ([]byte, error) {
	return e.command(c).CombinedOutput()
}

// command builds the *exec.Cmd that runs c's argv over `ssh <alias> ...`. It is
// exactly wrap(c.Args): the leading element is sshBin, so it is used as the
// binary (args[0]) and the rest as argv — never re-prepended. Re-prepending
// sshBin here would make ssh treat the literal "ssh" as the hostname.
func (e sshExecutor) command(c *exec.Cmd) *exec.Cmd {
	args := e.wrap(c.Args)
	return exec.Command(args[0], args[1:]...)
}

// wrap returns the full argv to run: `ssh <alias> <shell-joined-and-quoted args>`.
// Extracted so tests can assert the wrapping without launching ssh. The
// leading element is sshBin so callers can run the result directly as
// exec.Command(args[0], args[1:]...) without re-prepending sshBin (which
// would make ssh treat the literal "ssh" as the hostname).
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

// command builds the *exec.Cmd that runs script on the remote host as
// `ssh <alias> <script>`. The script is passed verbatim to the remote shell,
// which is what expands ~ and parses the script (so paths like ~-relative
// worktrees resolve on the right machine). Extracted so tests can assert the
// wrapping without launching ssh — never re-prepend sshBin here (that was the
// double-"ssh" bug; the leading element is the binary, the rest is argv).
func (f sshFS) command(script string) *exec.Cmd {
	return exec.Command("ssh", f.alias, script)
}

// statScript builds the remote shell test for Stat: emit dir/file/missing so
// the caller gets existence + IsDir in a single round-trip. Pure (no f.alias)
// so it's unit-testable independently of the transport.
func statScript(name string) string {
	return fmt.Sprintf("if [ -d %s ]; then echo dir; elif [ -e %s ]; then echo file; else echo missing; fi",
		shellQuote(name), shellQuote(name))
}

// parseStat interprets statScript's output. Pure so the dir/file/missing
// dispatch is unit-testable without an ssh round-trip. os.IsNotExist is
// checked by IsValidWorktree/Cleanup, so the missing branch returns the
// os.ErrNotExist sentinel (matching LocalFS).
func parseStat(name, out string) (os.FileInfo, error) {
	switch out {
	case "dir":
		return minimalFileInfo{name: name, isDir: true}, nil
	case "file":
		return minimalFileInfo{name: name, isDir: false}, nil
	default:
		return nil, errNotExist(name)
	}
}

func (f sshFS) Stat(name string) (os.FileInfo, error) {
	out, err := f.command(statScript(name)).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ssh stat %s: %w (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return parseStat(name, strings.TrimSpace(string(out)))
}

func (f sshFS) RemoveAll(path string) error {
	out, err := f.command("rm -rf -- " + shellQuote(path)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh rm %s: %w (%s)", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (f sshFS) MkdirAll(path string, perm os.FileMode) error {
	out, err := f.command("mkdir -p -- " + shellQuote(path)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh mkdir %s: %w (%s)", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// readdirScript builds the remote listing command: one path per entry,
// null-delimited so names with spaces/newlines survive. Pure so it's
// unit-testable independently of the transport.
func readdirScript(name string) string {
	return fmt.Sprintf("find %s -mindepth 1 -maxdepth 1 -print0", shellQuote(name))
}

// parseDirEntries splits null-delimited `find -print0` output into entries.
// Pure so the splitting is unit-testable without an ssh round-trip.
func parseDirEntries(out string) []os.DirEntry {
	var entries []os.DirEntry
	for _, p := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
		if p == "" {
			continue
		}
		entries = append(entries, dirEntry{name: p})
	}
	return entries
}

func (f sshFS) ReadDir(name string) ([]os.DirEntry, error) {
	out, err := f.command(readdirScript(name)).Output()
	if err != nil {
		return nil, fmt.Errorf("ssh readdir %s: %w", name, err)
	}
	return parseDirEntries(string(out)), nil
}
