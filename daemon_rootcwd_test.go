package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDaemon_BootsFromRootCwd is the C2.6 acceptance test: the daemon has no
// filesystem-of-the-user dependency. It boots and serves `list_instances`
// with its working directory set to "/" — a directory that is not a git repo
// and (on most systems) not writable. Before Phase 2 the daemon derived a
// protected branch from os.Getwd() via `git -C $cwd rev-parse --abbrev-ref
// HEAD`; from cwd "/" that would fail (not a repo) and, more importantly, the
// coupling was meaningless for a service process. This test proves the
// coupling is gone: the daemon boots clean and serves the control socket
// from a cwd that has no repo and no user files.
//
// It is the acceptance test for the whole phase: it exercises the protected
// store (loaded at boot with no cwd-derived branch), the kernel construction
// (no second constructor), and the control socket all at once.
func TestDaemon_BootsFromRootCwd(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: builds + runs the binary")
	}

	// Build a throwaway binary.
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "cs2-rootcwd-test")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = mustRepoRoot(t)
	out, err := build.CombinedOutput()
	require.NoErrorf(t, err, "go build: %s", out)

	// Isolate HOME so we don't touch the user's real ~/.cs2 state. Use a
	// short path under /tmp (not t.TempDir()) because the kernel's unix
	// domain socket lives at $HOME/.cs2/ctl.sock, and on macOS a socket path
	// longer than ~104 chars fails to bind with EINVAL. t.TempDir() nests
	// under /var/folders/<long-hash>/T/<TestName><rand>/... which blows the
	// limit; a short /tmp path keeps it well under.
	home := filepath.Join(os.TempDir(), "cs2-rootcwd-home")
	_ = os.RemoveAll(home)
	require.NoError(t, os.MkdirAll(home, 0o755))
	t.Setenv("HOME", home)
	t.Cleanup(func() { _ = os.RemoveAll(home) })

	// Start the daemon with cwd = "/". This is the crux of C2.6: the daemon
	// must not depend on its working directory.
	dmn := exec.Command(binPath, "daemon", "run")
	dmn.Dir = "/"
	dmn.Env = append(os.Environ(), "HOME="+home)
	var dmnStderr strings.Builder
	dmn.Stderr = &dmnStderr
	dmn.Stdout = &dmnStderr
	require.NoError(t, dmn.Start())
	t.Cleanup(func() {
		_ = dmn.Process.Kill()
		if t.Failed() {
			t.Logf("daemon stderr/stdout from cwd /:\n%s", dmnStderr.String())
		}
	})

	// Wait for the control socket (the kernel server binds it at boot).
	socket := filepath.Join(home, ".cs2", "ctl.sock")
	require.Eventually(t, func() bool {
		// If the daemon died, surface its output immediately rather than
		// waiting out the full timeout.
		if dmn.ProcessState != nil {
			t.Fatalf("daemon exited early (status %v):\n%s", dmn.ProcessState, dmnStderr.String())
		}
		_, err := os.Stat(socket)
		return err == nil
	}, 5*time.Second, 50*time.Millisecond, "daemon socket must come up from cwd /")

	// The socket file existing is not enough (could be a crashed daemon's
	// leftover). Drive a real syscall through `cs2 ctl list_instances`,
	// ALSO from cwd "/" to be strict: the client must work from a non-repo
	// cwd too.
	list := exec.Command(binPath, "ctl", "list_instances")
	list.Dir = "/"
	list.Env = append(os.Environ(), "HOME="+home)
	stdout, err := list.Output()
	require.NoError(t, err, "ctl list_instances must succeed against a daemon booted from cwd /")

	// A fresh state lists an empty fleet. The point is that the call works
	// at all from cwd "/" — the JSON shape is asserted elsewhere.
	trimmed := strings.TrimSpace(string(stdout))
	var v interface{}
	require.NoError(t, json.Unmarshal([]byte(trimmed), &v),
		"list_instances stdout must be valid JSON from cwd /: %q", trimmed)
	assert.Equal(t, "[]", trimmed, "a fresh state lists an empty fleet")
}
