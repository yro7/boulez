package host

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSSHHost_NameAndPolicy verifies the identity + AutoYes policy for a
// remote host. AutoYes must be off by default (decision 3) — auto-approving
// agent actions on a remote/prod box is riskier than locally.
func TestSSHHost_NameAndPolicy(t *testing.T) {
	h := NewSSHHost("dev-machine")
	assert.Equal(t, "dev-machine", h.Name())
	assert.Equal(t, "dev-machine", h.Alias())
	assert.False(t, h.AutoYesDefault(), "remote AutoYes must default to off")
}

// TestSSHHost_WorktreeDir verifies the worktree dir is the ABSOLUTE
// <remote-$HOME>/.boulez/worktrees: $HOME is resolved on the remote at
// Start, not a ~-relative literal. A ~-relative literal never reached git as
// an absolute path (single-quoted argv suppresses tilde expansion; git never
// expands ~), which created a literal `~` dir inside the repo and left the
// stored worktree path unusable — the remote "not a git repository" bug.
// We stub remoteHome so no ssh is launched.
func TestSSHHost_WorktreeDir(t *testing.T) {
	orig := remoteHome
	defer func() { remoteHome = orig }()

	var gotAlias string
	remoteHome = func(alias, socket string) (string, error) {
		gotAlias = alias
		return "/root", nil
	}

	dir, err := NewSSHHost("dev-machine").WorktreeDir()
	require.NoError(t, err)
	assert.Equal(t, "dev-machine", gotAlias, "WorktreeDir must resolve $HOME on the remote via remoteHome(alias)")
	assert.Equal(t, "/root/.boulez/worktrees", dir,
		"dir must be the absolute <home>/.boulez/worktrees, not a ~-relative literal")
}

// TestSSHHost_WorktreeDir_PropagatesRemoteHomeError proves a $HOME resolution
// failure surfaces from WorktreeDir (rather than silently falling back to a
// broken ~-relative path). A silent fallback would hide an unreachable host
// behind a later, misleading "not a git repository" error.
func TestSSHHost_WorktreeDir_PropagatesRemoteHomeError(t *testing.T) {
	orig := remoteHome
	defer func() { remoteHome = orig }()
	remoteHome = func(alias, socket string) (string, error) {
		return "", fmt.Errorf("boom")
	}

	_, err := NewSSHHost("h").WorktreeDir()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

// TestWorktreeDirForHome is the pure unit test for the path-construction half
// of WorktreeDir, independent of the ssh round-trip.
func TestWorktreeDirForHome(t *testing.T) {
	assert.Equal(t, "/root/.boulez/worktrees", worktreeDirForHome("/root"))
	assert.Equal(t, "/home/me/.boulez/worktrees", worktreeDirForHome("/home/me"))
}

// sshArgv builds an expected slave argv: `ssh <hardening opts> <tail...>`.
// Non-interactive slaves always carry sshHardenArgs (BatchMode + ConnectTimeout)
// right after the binary; referencing the production helper keeps these
// expectations in lockstep if the hardening set changes.
func sshArgv(tail ...string) []string {
	return append(append([]string{"ssh"}, sshHardenArgs()...), tail...)
}

// TestSSHExecutor_Wrap_InjectsControlPath proves that when a master socket is
// configured (i.e. EnsureConnected has armed muxing), the slave command carries
// -o ControlPath=<socket> so it rides the master. With socket="" the option is
// absent (plain one-shot ssh) — the non-muxing fallback path. This is the
// property that eliminates one-shot connection hammering on remote hosts.
func TestSSHExecutor_Wrap_InjectsControlPath(t *testing.T) {
	orig := exec.Command("git", "status")

	// Muxing armed: -o ControlPath=<socket> precedes the alias.
	withSock := sshExecutor{alias: "dev-machine", socket: "/tmp/x.sock"}.wrap(orig.Args)
	assert.Equal(t,
		sshArgv("-o", "ControlPath=/tmp/x.sock", "dev-machine", "'git' 'status'"),
		withSock)

	// Muxing unarmed: no control option, plain one-shot ssh.
	noSock := sshExecutor{alias: "dev-machine"}.wrap(orig.Args)
	assert.Equal(t, sshArgv("dev-machine", "'git' 'status'"), noSock)
}

// TestSSHFS_CommandInjectsControlPath proves the FS seam rides the master too.
func TestSSHFS_CommandInjectsControlPath(t *testing.T) {
	withSock := sshFS{alias: "dev-machine", socket: "/tmp/x.sock"}.command("echo hi")
	assert.Equal(t,
		sshArgv("-o", "ControlPath=/tmp/x.sock", "dev-machine", "echo hi"),
		withSock.Args)

	noSock := sshFS{alias: "dev-machine"}.command("echo hi")
	assert.Equal(t, sshArgv("dev-machine", "echo hi"), noSock.Args)
}

// TestSSHPtyFactory_CommandInjectsControlPath proves the PTY seam rides the
// master (the -t comes before the control opts).
func TestSSHPtyFactory_CommandInjectsControlPath(t *testing.T) {
	orig := exec.Command("tmux", "attach-session", "-t", "foo")
	withSock := sshPtyFactory{alias: "dev-machine", socket: "/tmp/x.sock"}.command(orig)
	assert.Equal(t,
		[]string{"ssh", "-t", "-o", "ControlPath=/tmp/x.sock", "dev-machine", "'tmux' 'attach-session' '-t' 'foo'"},
		withSock.Args)

	noSock := sshPtyFactory{alias: "dev-machine"}.command(orig)
	assert.Equal(t, []string{"ssh", "-t", "dev-machine", "'tmux' 'attach-session' '-t' 'foo'"}, noSock.Args)
}

// TestSSHHost_EnsureConnected_Delegates proves EnsureConnected drives the
// master lifecycle (and is best-effort: a start failure is swallowed so Start
// is never aborted by a transport hiccup). The master uses a fake runner so
// no real ssh is launched.
func TestSSHHost_EnsureConnected_Delegates(t *testing.T) {
	orig := sshControlDir
	sshControlDir = func() (string, error) { return t.TempDir(), nil }
	t.Cleanup(func() { sshControlDir = orig })

	h := NewSSHHost("dev-machine")
	h.master = h.master.withRunner(&fakeSSHRunner{up: true}) // master already up
	require.NoError(t, h.EnsureConnected())

	// Start failure is swallowed (best-effort).
	h2 := NewSSHHost("dev-machine")
	h2.master = h2.master.withRunner(&fakeSSHRunner{up: false, startOK: false})
	require.NoError(t, h2.EnsureConnected(), "EnsureConnected must not abort on master failure")
}

// TestSSHExecutor_Wrap proves the seam: every command is wrapped as
// `ssh <alias> <shell-quoted args>`. This is the guarantee that v2 routes git
// over ssh without touching the git package — the executor does the wrapping.
func TestSSHExecutor_Wrap(t *testing.T) {
	e := sshExecutor{alias: "dev-machine"}

	orig := exec.Command("git", "-C", "/repo", "status", "--porcelain")
	got := e.wrap(orig.Args)

	// Every arg is shell-quoted (even safe words like "git") — conservative but
	// correct. The joined string re-parses back to the original args.
	require.Equal(t,
		sshArgv("dev-machine", "'git' '-C' '/repo' 'status' '--porcelain'"),
		got)

	// And it round-trips back to the original args via a POSIX shell. The joined
	// command is the last argv element (after ssh, the hardening opts, and the
	// alias).
	assert.Equal(t, orig.Args, shellReparse(t, got[len(got)-1]))
}

// TestSSHExecutor_Wrap_Quoting proves args survive the remote shell: a path
// with a space stays a single arg after the remote shell re-parses. This is
// the safety-critical property — without it, a
// repo path like `/home/me/my repo` would split into two args remotely.
// We check the round-trip (the real property ssh relies on) rather than
// pinning the exact quoting of safe words.
func TestSSHExecutor_Wrap_Quoting(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"simple", []string{"git", "status"}},
		{"path with space", []string{"git", "-C", "/home/me/my repo", "status"}},
		{"path with single quote", []string{"git", "-C", "/a/b'c"}},
		{"dollar metachar", []string{"sh", "-c", "echo $HOME"}},
		{"backtick", []string{"sh", "-c", "echo `whoami`"}},
		// Injection vectors: each must stay a single arg so it cannot break out
		// of the remote shell. The round-trip below is the real safety property.
		{"command substitution", []string{"sh", "-c", "$(reboot)"}},
		{"command separator", []string{"git", "-C", "/repo; rm -rf /", "status"}},
		{"pipe", []string{"git", "-C", "/repo | cat", "status"}},
		{"redirect", []string{"git", "-C", "/repo > /etc/passwd", "status"}},
		{"newline", []string{"git", "-C", "/repo\nrm -rf /", "status"}},
		{"empty arg", []string{"git", "", "status"}},
		{"leading dash arg injection", []string{"git", "-C", "--upload-pack=evil", "status"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sshExecutor{alias: "h"}.wrap(tc.args)
			// argv shape: ssh <hardening opts> <alias> <joined>. The alias is
			// second-to-last and the joined command is last.
			require.GreaterOrEqual(t, len(got), 3)
			assert.Equal(t, "ssh", got[0])
			assert.Equal(t, "h", got[len(got)-2])

			// Round-trip: a real POSIX shell must re-parse the joined string
			// back into the original args. This is exactly what ssh's remote
			// shell does, so it's a faithful end-to-end check of the quoting —
			// even a path with a space or a quote stays one arg remotely.
			reparsed := shellReparse(t, got[len(got)-1])
			assert.Equal(t, tc.args, reparsed,
				"joined %q must re-parse to original args", got[2])
		})
	}
}

// shellReparse parses `joined` the way a POSIX shell would, returning the
// resulting argv. Uses the local sh (the same parser ssh's remote shell is
// compatible with). Output is null-delimited so args containing newlines
// round-trip correctly (a newline inside a single-quoted arg stays one arg).
func shellReparse(t *testing.T, joined string) []string {
	t.Helper()
	cmd := exec.Command("sh", "-c",
		`eval "set -- $1"; for a in "$@"; do printf '%s\0' "$a"; done`, "_", joined)
	out, err := cmd.Output()
	require.NoErrorf(t, err, "sh eval-reparse of %q failed: %s", joined, out)
	parts := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	if len(parts) == 1 && parts[0] == "" {
		return nil
	}
	return parts
}

// TestSSHExecutor_RunBuildsWrapArgv is the regression guard for the
// double-"ssh" bug: Run/Output/CombinedOutput must execute exactly wrap's
// argv, not `ssh ssh <alias> ...`. Re-prepending sshBin made ssh resolve the
// literal "ssh" as the hostname (exit 255), which surfaced as
// "not a git repository" at instance creation. We assert the built command's
// Args equal wrap(c.Args) — no more, no less.
func TestSSHExecutor_RunBuildsWrapArgv(t *testing.T) {
	e := sshExecutor{alias: "dev-machine"}
	orig := exec.Command("git", "-C", "/repo", "status", "--porcelain")
	built := e.command(orig)
	assert.Equal(t, e.wrap(orig.Args), built.Args,
		"executor must run exactly wrap's argv; never re-prepend sshBin")
}

// TestShellQuote_EdgeCases pins the quoting helper on tricky inputs.
func TestShellQuote_EdgeCases(t *testing.T) {
	assert.Equal(t, "''", shellQuote(""))
	assert.Equal(t, "'simple'", shellQuote("simple"))
	assert.Equal(t, `'with space'`, shellQuote("with space"))
	assert.Equal(t, `'it'\''s'`, shellQuote("it's"))
}

// TestSSHHost_ResolveRepoPath_Passthrough proves the remote branch of
// transport-specific path resolution: SSHHost returns the path unchanged so
// the remote shell resolves ~ and relative paths against the remote $HOME.
// Resolving locally (filepath.Abs) would point at the wrong machine — the
// "not a git repository" bug. This is the counterpart to LocalHost's
// absolutizing (TestLocalHost_ResolveRepoPath_Absolutizes).
func TestSSHHost_ResolveRepoPath_Passthrough(t *testing.T) {
	h := NewSSHHost("dev-machine")
	cases := []string{
		"/home/freebox/testgit", // absolute
		"testgit",               // relative — must reach the remote shell as-is
		"~/repos/proj",          // ~-relative — expanded remotely, not here
		"./foo/../bar",          // dirty relative — remote shell cleans it
	}
	for _, p := range cases {
		assert.Equal(t, p, h.ResolveRepoPath(p),
			"remote path must be returned unchanged so the remote shell resolves it")
	}
}

// TestSSHFS_CommandBuildsArgv is the regression guard for the double-"ssh" bug
// on the FS seam (the executor seam is guarded by
// TestSSHExecutor_RunBuildsWrapArgv). command() must build exactly
// `ssh <alias> <script>`, never re-prepend sshBin. We assert the built
// command's Args — without launching ssh — so a re-introduction of the bug
// class fails loudly instead of silently corrupting every sshFS op.
func TestSSHFS_CommandBuildsArgv(t *testing.T) {
	f := sshFS{alias: "dev-machine"}
	built := f.command("echo hi")
	assert.Equal(t, sshArgv("dev-machine", "echo hi"), built.Args,
		"sshFS.command must build exactly [ssh <hardening opts> alias script]; never re-prepend sshBin")
}

// TestSSHFS_StatScript_QuotesPath proves the remote test script quotes the
// path (so a path with a space or a metachar survives the remote shell and
// cannot break out into a second command). Pure (no alias), so it's
// unit-testable independently of the transport.
func TestSSHFS_StatScript_QuotesPath(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"simple", "/repo"},
		{"space", "/home/me/my repo"},
		{"~", "~/worktrees/x"},
		{"relative", "testgit"},
		{"injection", "/repo; rm -rf /"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			script := statScript(tc.path)
			// The path must appear inside single quotes in the script, so the
			// remote shell treats it as a literal (no expansion of ; / space / ~).
			assert.Contains(t, script, shellQuote(tc.path),
				"path must be shell-quoted in the stat script")
			// And the quoted path must round-trip back to the original via a
			// POSIX shell (the real safety property the remote shell relies on).
			assert.Equal(t, tc.path, shellReparse(t, shellQuote(tc.path))[0])
		})
	}
}

// TestSSHFS_ParseStat_Dispatch proves the dir/file/missing dispatch is correct
// without an ssh round-trip. os.IsNotExist must hold for the missing branch
// (IsValidWorktree/Cleanup rely on it to detect orphaned worktrees).
func TestSSHFS_ParseStat_Dispatch(t *testing.T) {
	info, err := parseStat("/p", "dir")
	require.NoError(t, err)
	assert.True(t, info.IsDir(), "'dir' -> IsDir")
	assert.Equal(t, "/p", info.Name())

	info, err = parseStat("/p", "file")
	require.NoError(t, err)
	assert.False(t, info.IsDir(), "'file' -> not IsDir")
	assert.Equal(t, "/p", info.Name())

	_, err = parseStat("/p", "missing")
	assert.True(t, os.IsNotExist(err), "'missing' must satisfy os.IsNotExist")

	// Defensive: unexpected output is treated as not-exist (not a crash).
	_, err = parseStat("/p", "garbage")
	assert.True(t, os.IsNotExist(err), "unexpected output -> os.IsNotExist")
}

// TestSSHFS_ReaddirScript_QuotesPath proves the remote find script quotes the
// path. Pure so it's unit-testable independently of the transport.
func TestSSHFS_ReaddirScript_QuotesPath(t *testing.T) {
	script := readdirScript("/home/me/my repo")
	assert.Contains(t, script, shellQuote("/home/me/my repo"),
		"path must be shell-quoted in the readdir script")
	assert.Contains(t, script, "-print0", "readdir must null-delimit to survive spaces")
}

// TestSSHFS_ParseDirEntries_SplitsNullDelimited proves the find -print0 output
// is split correctly: one entry per name, names with spaces/newlines survive.
// Pure so it's unit-testable without an ssh round-trip.
func TestSSHFS_ParseDirEntries_SplitsNullDelimited(t *testing.T) {
	// Empty / no entries.
	assert.Nil(t, parseDirEntries(""))
	assert.Nil(t, parseDirEntries("\x00"))

	// Single entry.
	e := parseDirEntries("a\x00")
	require.Len(t, e, 1)
	assert.Equal(t, "a", e[0].Name())

	// Multiple entries, one with a space.
	e = parseDirEntries("a\x00b c\x00d\x00")
	require.Len(t, e, 3)
	assert.Equal(t, "a", e[0].Name())
	assert.Equal(t, "b c", e[1].Name(), "name with space must survive")
	assert.Equal(t, "d", e[2].Name())

	// A newline inside a name survives (find -print0 null-delimits, not
	// newline-delimited — this is the reason -print0 is used).
	e = parseDirEntries("x\ny\x00")
	require.Len(t, e, 1)
	assert.Equal(t, "x\ny", e[0].Name(), "name with newline must survive")
}

// TestSSHPtyFactory_CommandBuildsArgv is the regression guard for the
// double-"ssh" bug on the PTY seam. command() must build exactly
// `ssh -t <alias> <shell-joined args>`, never re-prepend sshBin. We assert
// the built command's Args — without launching ssh or allocating a PTY — so
// a re-introduction of the bug class fails loudly.
func TestSSHPtyFactory_CommandBuildsArgv(t *testing.T) {
	f := sshPtyFactory{alias: "dev-machine"}
	orig := exec.Command("tmux", "attach-session", "-t", "foo")
	built := f.command(orig)

	// Args[0] is sshBin exactly once; Args[1] is -t; Args[2] is the alias;
	// Args[3] is the shell-joined-and-quoted original args, which must
	// round-trip back to the original args via a POSIX shell.
	require.Equal(t, []string{"ssh", "-t", "dev-machine", "'tmux' 'attach-session' '-t' 'foo'"}, built.Args)
	assert.Equal(t, orig.Args, shellReparse(t, built.Args[3]),
		"joined args must re-parse to the original args")
}

// TestSSHPtyFactory_Command_Quoting proves args survive the remote shell: a
// session name with a space stays a single arg. Same property as the
// executor's quoting, on the PTY seam.
func TestSSHPtyFactory_Command_Quoting(t *testing.T) {
	orig := exec.Command("tmux", "attach-session", "-t", "my session")
	built := sshPtyFactory{alias: "h"}.command(orig)
	require.Len(t, built.Args, 4)
	assert.Equal(t, orig.Args, shellReparse(t, built.Args[3]))
}

// TestSSHHost_AttachCmd_BuildsArgv proves AttachCmd produces the interactive
// attach command: `ssh -t [control opts] <alias> tmux attach-session -t <name>`.
// It rides the master when a socket is set (mirroring sshPtyFactory.command)
// and omits the control opts when socket is "". The -t forces a remote PTY so
// the attach is interactive under tea.ExecProcess. Asserted without launching
// ssh — the argv is the contract.
func TestSSHHost_AttachCmd_BuildsArgv(t *testing.T) {
	h := NewSSHHost("dev-machine")
	h.master = sshMaster{alias: "dev-machine", socket: "/tmp/x.sock"}

	withSock := h.AttachCmd("foo")
	assert.Equal(t,
		[]string{"ssh", "-t", "-o", "ControlPath=/tmp/x.sock", "dev-machine", "'tmux' 'attach-session' '-t' 'foo'"},
		withSock.Args)

	h.master = sshMaster{alias: "dev-machine"} // socket "" => plain one-shot
	noSock := h.AttachCmd("foo")
	assert.Equal(t,
		[]string{"ssh", "-t", "dev-machine", "'tmux' 'attach-session' '-t' 'foo'"},
		noSock.Args)
}

// TestSSHHost_AttachCmd_Quoting proves a session name with a space survives the
// remote shell round-trip (stays a single arg), the same property
// sshPtyFactory.command enjoys.
func TestSSHHost_AttachCmd_Quoting(t *testing.T) {
	h := NewSSHHost("dev-machine")
	h.master = sshMaster{alias: "dev-machine"} // socket "" => no control opts
	built := h.AttachCmd("my session")
	require.Len(t, built.Args, 4)
	assert.Equal(t,
		[]string{"tmux", "attach-session", "-t", "my session"},
		shellReparse(t, built.Args[3]))
}
