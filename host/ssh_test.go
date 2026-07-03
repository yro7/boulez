package host

import (
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

// TestSSHHost_WorktreeDir verifies the worktree dir is the ~-relative literal
// (decision A): expanded by the remote shell, no $HOME resolution round-trip.
func TestSSHHost_WorktreeDir(t *testing.T) {
	dir, err := NewSSHHost("h").WorktreeDir()
	require.NoError(t, err)
	assert.Equal(t, "~/.cs2/worktrees", dir)
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
		[]string{"ssh", "dev-machine", "'git' '-C' '/repo' 'status' '--porcelain'"},
		got)

	// And it round-trips back to the original args via a POSIX shell.
	assert.Equal(t, orig.Args, shellReparse(t, got[2]))
}

// TestSSHExecutor_Wrap_Quoting proves args survive the remote shell: a path
// with a space stays a single arg after the remote shell re-parses. This is
// the safety-critical property (PLAN-ssh-v2.md decision 7) — without it, a
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
			require.Len(t, got, 3)
			assert.Equal(t, "ssh", got[0])
			assert.Equal(t, "h", got[1])

			// Round-trip: a real POSIX shell must re-parse the joined string
			// back into the original args. This is exactly what ssh's remote
			// shell does, so it's a faithful end-to-end check of the quoting —
			// even a path with a space or a quote stays one arg remotely.
			reparsed := shellReparse(t, got[2])
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

// TestShellQuote_EdgeCases pins the quoting helper on tricky inputs.
func TestShellQuote_EdgeCases(t *testing.T) {
	assert.Equal(t, "''", shellQuote(""))
	assert.Equal(t, "'simple'", shellQuote("simple"))
	assert.Equal(t, `'with space'`, shellQuote("with space"))
	assert.Equal(t, `'it'\''s'`, shellQuote("it's"))
}
