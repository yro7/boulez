package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/yro7/boulez/cmd"
	"github.com/yro7/boulez/log"
	"github.com/yro7/boulez/session/fs"
)

// SSHHost runs an instance's environment on a remote machine via the system
// ssh binary. It relies on the user's ssh config (~/.ssh/config), agent, and
// keys — boulez never stores credentials. Every command, filesystem operation,
// and PTY is routed over `ssh <alias> ...`.
//
// To avoid hammering one-shot `ssh` connections (each a fresh TCP+auth round-
// trip that re-attempts every LocalForward in the alias's config, causing port-
// collision spam and racing short tmux poll timeouts), SSHHost keeps a
// long-lived ControlMaster (sshMaster) per alias. Slave commands carry
// `-o ControlPath=<socket>` and transparently multiplex over it. See
// ssh_conn.go for the full rationale and the empirically-verified fallback.
//
// The alias is an entry the user has configured in their ssh config / known
// hosts (e.g. "dev-machine", "gpu-box"). boulez treats it as opaque; resolving
// it to a host/user/port is ssh's job.
type SSHHost struct {
	alias  string
	master sshMaster
}

// Compile-time guarantee that SSHHost satisfies Host.
var _ Host = SSHHost{}

// NewSSHHost returns an SSHHost bound to the given ssh alias. The ControlMaster
// socket path is computed eagerly (does not require the master to be running);
// the master itself is started lazily by EnsureConnected at instance Start.
func NewSSHHost(alias string) SSHHost {
	m := newSSHMaster(alias)
	return SSHHost{alias: alias, master: m}
}

// Alias returns the ssh alias this host connects to.
func (h SSHHost) Alias() string { return h.alias }

// Name implements Host: the ssh alias. Used for InstanceData persistence and
// TUI display only — never in commit messages, branch names, or tmux session
// names (PII discipline).
func (h SSHHost) Name() string { return h.alias }

// AutoYesDefault implements Host: false. AutoYes is off by default on remote
// hosts — auto-approving agent actions on a shared/prod box is riskier than
// locally (decision 3). The user can still toggle it on per-instance.
func (h SSHHost) AutoYesDefault() bool { return false }

// EnsureConnected implements Host: start (or verify) a long-lived ControlMaster
// for this alias so the burst of git/tmux commands about to run — and the
// daemon's per-second poll loop thereafter — multiplex over one connection
// instead of opening one-shot `ssh` connections. Best-effort: a failure is
// logged as a warning and boulez falls back to one-shot ssh (slaves' ControlPath
// then finds no master and opens direct connections). Never fatal to Start.
// LocalHost's implementation is a no-op.
func (h SSHHost) EnsureConnected() error {
	if err := h.master.Ensure(); err != nil {
		log.WarningLog.Printf("ssh master %s: %v (falling back to one-shot connections)", h.alias, err)
		return nil // best-effort: do not abort Start
	}
	return nil
}

// Executor implements Host: an executor that prefixes `ssh <alias>` to every
// command (with -o ControlPath=<socket> so it multiplexes over the master).
func (h SSHHost) Executor() cmd.Executor {
	return sshExecutor{alias: h.alias, socket: h.master.socketForSlave()}
}

// FS implements Host: a filesystem routed over ssh (with -o ControlPath=<socket>
// for master multiplexing). Paths reaching it
// (worktreeDir, worktreePath) are absolute on both transports — the Host
// resolves $HOME remotely for SSH — so no ~ expansion is relied upon
// (single-quoted argv would suppress it anyway).
func (h SSHHost) FS() fs.FS { return sshFS{alias: h.alias, socket: h.master.socketForSlave()} }

// WorktreeDir implements Host: the absolute <remote-$HOME>/.boulez/worktrees
// directory, with $HOME resolved on the remote host. $HOME is resolved over a
// master-multiplexed connection (carrying -o ControlPath) so it does not open a
// stray one-shot connection that would re-bind the alias's LocalForwards. The
// path is ABSOLUTE
// (not ~-relative) because every consumer passes it through single-quoted
// argv (joinShellQuoted) or `git -C`, neither of which expands `~`: the remote
// shell can't (single quotes suppress tilde expansion) and git never does.
// A ~-relative literal therefore reached git as `~/.boulez/...` verbatim,
// which git treated as a relative path under the repo — creating a literal `~`
// directory inside the repo and leaving the stored worktree path unusable
// (the "fatal: cannot change to '~/.boulez/...'" / "not a git repository"
// bug at remote-instance creation). Resolving $HOME once at Start yields a
// real absolute path that flows unchanged through quoting, git -C, tmux -c,
// and the agent's cwd. One ssh round-trip per Start/Restore.
func (h SSHHost) WorktreeDir() (string, error) {
	home, err := remoteHome(h.alias, h.master.socketForSlave())
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", fmt.Errorf("ssh resolve $HOME for %s: remote home directory is empty", h.alias)
	}
	return worktreeDirForHome(home), nil
}

// remoteHome resolves the remote login directory ($HOME) of the ssh alias via a
// single `ssh [opts] <alias> printf %s "$HOME"` round-trip. The ControlPath
// option (when socket != "") makes this ride the master connection rather than
// opening a one-shot that would re-bind the alias's LocalForwards. "$HOME" is
// double-quoted in the script so the REMOTE shell expands it — this must NOT
// go through joinShellQuoted, which single-quotes every arg and would freeze
// "$HOME" as a literal. Swappable (package var) so tests can stub the network
// hop and assert WorktreeDir's wiring without launching ssh.
var remoteHome = func(alias, socket string) (string, error) {
	args := append([]string{}, sshHardenArgs()...)
	args = append(args, sshControlArgs(socket)...)
	args = append(args, alias, `printf %s "$HOME"`)
	out, err := exec.Command(sshBin, args...).Output()
	if err != nil {
		return "", fmt.Errorf("ssh resolve $HOME for %s: %w", alias, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// worktreeDirForHome returns the absolute worktree directory under a resolved
// remote home. Pure so the path construction is unit-testable without an ssh
// round-trip.
func worktreeDirForHome(home string) string {
	return filepath.Join(home, ".boulez", "worktrees")
}

// ResolveRepoPath implements Host: a remote repo path is returned unchanged so
// the remote shell resolves it. Relative paths and ~ expand against the
// remote $HOME (ssh non-interactive sessions start there, stably across
// invocations) — BUT only when the path reaches the remote shell unquoted.
// It does for repo paths (passed as a single `git -C <path>` arg that the
// remote shell re-parses), so ~ and relatives resolve remotely. Resolving
// locally with filepath.Abs would produce a path on the wrong machine (e.g.
// /Users/local/.../testgit), which is exactly the bug where a remote relative
// path failed as "not a git repository". (Worktree paths are different: see
// WorktreeDir — they are made absolute via remote $HOME resolution because
// they flow through single-quoted argv where ~ does NOT expand.)
func (h SSHHost) ResolveRepoPath(path string) string { return path }

// AttachCmd implements Host: the *exec.Cmd that interactively attaches the
// user's terminal to a remote tmux session, run via tea.ExecProcess by the
// TUI. The argv is `ssh -t [control opts] <alias> tmux <attachTmuxArgv>` —
// reusing sshInteractiveArgs so the -t / control-opts / shell-joining
// assembly is shared (and its tests). The remote tmux binds Ctrl-Q to
// detach-client for the duration of the attach (then unbinds), preserving
// boulez's Ctrl-Q detach contract now that the manual stdin scavenger is
// gone (see attachTmuxArgv). No PTY is allocated by boulez: the local
// terminal is already a tty, ssh -t allocates the remote one, and
// tea.ExecProcess releases the Bubbletea terminal for the command's duration.
func (h SSHHost) AttachCmd(sessionName string) *exec.Cmd {
	args := sshInteractiveArgs(h.alias, h.master.socketForSlave(),
		append([]string{"tmux"}, attachTmuxArgv(sessionName)...))
	return exec.Command(args[0], args[1:]...)
}

// --- Executor ---

// sshBin is the binary name used to connect. Constant so tests can assert
// against it without hardcoding "ssh" in two places.
const sshBin = "ssh"

// sshExecutor wraps every command in `ssh <alias> <cmd...>` (with
// -o ControlPath=<socket> so it multiplexes over the master when one is up).
// Because ssh joins argv with spaces and re-parses via the remote shell, each
// original arg is shell-quoted to survive the round-trip (a path with a space
// stays one arg).
type sshExecutor struct {
	alias  string
	socket string // control socket; "" => plain one-shot ssh (no muxing)
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

// wrap returns the full argv to run: `ssh [control opts] <alias> <shell-joined-and-quoted args>`.
// Extracted so tests can assert the wrapping without launching ssh. The
// leading element is sshBin so callers can run the result directly as
// exec.Command(args[0], args[1:]...) without re-prepending sshBin (which
// would make ssh treat the literal "ssh" as the hostname). The control opts
// (-o ControlPath=<socket>) make the command ride the ControlMaster when one
// is up; when socket is "" they are omitted (plain one-shot ssh).
func (e sshExecutor) wrap(origArgs []string) []string {
	args := append([]string{sshBin}, sshHardenArgs()...)
	args = append(args, sshControlArgs(e.socket)...)
	args = append(args, e.alias, joinShellQuoted(origArgs))
	return args
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

// sshFS implements fs.FS by running shell commands over ssh. Paths reaching it
// (worktreeDir, worktreePath) are absolute on both transports, so no ~
// expansion is relied upon. Each operation is a single
// `ssh host sh -c '...'` invocation.
type sshFS struct {
	alias  string
	socket string // control socket; "" => plain one-shot ssh (no muxing)
}

// command builds the *exec.Cmd that runs script on the remote host as
// `ssh [control opts] <alias> <script>`. The control opts (-o ControlPath)
// make the command ride the ControlMaster when one is up; when socket is ""
// they are omitted (plain one-shot ssh). The script is passed verbatim to the
// remote shell, which parses it. Paths reaching sshFS are absolute (the Host
// resolves $HOME remotely for SSH), so no ~ expansion is needed (and the
// scripts also shell-quote paths, which would suppress ~ expansion anyway).
// Extracted so tests can assert the wrapping without launching ssh — never
// re-prepend sshBin here (that was the double-"ssh" bug; the leading element
// is the binary, the rest is argv).
func (f sshFS) command(script string) *exec.Cmd {
	args := append([]string{}, sshHardenArgs()...)
	args = append(args, sshControlArgs(f.socket)...)
	args = append(args, f.alias, script)
	return exec.Command(sshBin, args...)
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

// sshInteractiveArgs returns the full argv to run a command interactively
// over ssh with a remote PTY: `ssh -t [control opts] <alias> <shell-joined
// args>`. The -t forces PTY allocation on the remote so interactive commands
// (tmux attach-session) work; the control opts (-o ControlPath=<socket>) ride
// the ControlMaster when one is up (omitted when socket is ""). The single
// argv builder for SSHHost.AttachCmd; tested without launching ssh.
func sshInteractiveArgs(alias, socket string, cmdArgs []string) []string {
	args := []string{sshBin, "-t"}
	args = append(args, sshControlArgs(socket)...)
	args = append(args, alias, joinShellQuoted(cmdArgs))
	return args
}
