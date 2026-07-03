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

// TestCtl_StdoutIsPureJSON is the end-to-end regression test for the
// "wrote logs to ..." stdout pollution (finding #6). It builds the cs2
// binary, starts a daemon, runs `cs2 ctl list_instances`, and asserts the
// stdout is a single parseable JSON document with no trailing log line.
//
// Before the fix, log.Close() printed "wrote logs to /tmp/.../claudesquad.log"
// to stdout AFTER the JSON, so json.Unmarshal failed with "Extra data".
func TestCtl_StdoutIsPureJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: builds + runs the binary")
	}

	// Build a throwaway binary.
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "cs2ctl-test")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = mustRepoRoot(t)
	out, err := build.CombinedOutput()
	require.NoErrorf(t, err, "go build: %s", out)

	// Isolate HOME so we don't touch the user's real ~/.cs2 state.
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Start the daemon.
	daemon := exec.Command(binPath, "--daemon")
	daemon.Dir = mustRepoRoot(t)
	require.NoError(t, daemon.Start())
	t.Cleanup(func() { _ = daemon.Process.Kill() })

	// Wait for the socket (the daemon auto-launches the kernel server).
	socket := filepath.Join(home, ".cs2", "ctl.sock")
	require.Eventually(t, func() bool {
		_, err := os.Stat(socket)
		return err == nil
	}, 3*time.Second, 50*time.Millisecond, "daemon socket appeared")

	// Run `cs2 ctl list_instances` and capture stdout.
	cmd := exec.Command(binPath, "ctl", "list_instances")
	cmd.Env = append(os.Environ(), "HOME="+home)
	stdout, err := cmd.Output()
	require.NoError(t, err, "ctl list_instances failed")

	// The ENTIRE stdout must be a single JSON document. No trailing line.
	trimmed := strings.TrimSpace(string(stdout))
	require.NotEmpty(t, trimmed, "stdout should not be empty")
	var v interface{}
	require.NoError(t, json.Unmarshal([]byte(trimmed), &v),
		"stdout must be pure JSON (no 'wrote logs to ...' trailer): %q", trimmed)

	// And there must be no second line beyond the JSON.
	// A clean `[]` or `[{...}]` has no log line after it.
	assert.False(t, strings.Contains(trimmed, "wrote logs to"),
		"log path must not leak to stdout: %q", trimmed)
}

func mustRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	// file ends in .../cs2/<worktree>/ctl_stdout_test.go
	return filepath.Dir(file)
}
