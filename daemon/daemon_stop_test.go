package daemon

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yro7/boulez/kernel"
)

// startFakeDaemonSocket binds a unix socket at the kernel control-socket path
// (derived from $HOME, which the caller sets via t.Setenv) and accepts
// connections in the background so ProbeSocket reports the daemon as
// reachable. This stands in for a live daemon without spawning one — enough
// to exercise StopDaemon's "socket up" branch.
//
// $HOME is pointed at a SHORT path under /tmp because unix socket paths are
// capped at ~104 bytes on macOS, and t.TempDir()'s default location
// (/var/folders/.../T/<long-test-name>/001) blows past that limit.
func startFakeDaemonSocket(t *testing.T) func() {
	t.Helper()
	shortHome, err := os.MkdirTemp("/tmp", "bt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(shortHome) })
	t.Setenv("HOME", shortHome)

	socketPath, err := kernel.SocketPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(socketPath), 0o755))
	ln, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed — test teardown
			}
			_ = conn.Close()
		}
	}()
	return func() { _ = ln.Close() }
}

// TestStopDaemon_ErrorsWhenSocketUpButNoPID is the red test for the "stop
// lies" regression: when the daemon is serving on the control socket but no
// PID file exists (the real-world trigger: `boulez daemon run` started
// directly, which never wrote daemon.pid), StopDaemon used to return nil and
// the CLI printed "daemon stopped" — a lie. It must instead surface that the
// daemon appears alive but is unkillable from the recorded state.
func TestStopDaemon_ErrorsWhenSocketUpButNoPID(t *testing.T) {
	stop := startFakeDaemonSocket(t)
	defer stop()

	// Sanity: the socket is reachable (otherwise the test is testing nothing).
	require.NoError(t, ProbeSocket(), "fake daemon socket must be reachable")

	// No daemon.pid, no daemon.lock — exactly the user's situation.
	err := StopDaemon()
	require.Error(t, err, "StopDaemon must not claim success while the socket is up")
	assert.Contains(t, err.Error(), "running",
		"the error must say the daemon appears running so the user knows it did not stop")
}

// TestStopDaemon_SuccessWhenNoDaemonAndSocketDown pins the idempotent path:
// with no daemon running (socket down) and no PID files, StopDaemon returns
// nil — "nothing to stop" is a legitimate success, not an error.
func TestStopDaemon_SuccessWhenNoDaemonAndSocketDown(t *testing.T) {
	// Use a short HOME under /tmp for the same socket-path-length reason as
	// startFakeDaemonSocket; here we create no socket, just an empty config dir.
	shortHome, err := os.MkdirTemp("/tmp", "bt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(shortHome) })
	t.Setenv("HOME", shortHome)

	// No socket listener, no pid/lock files.
	require.Error(t, ProbeSocket(), "precondition: no daemon socket")
	assert.NoError(t, StopDaemon(), "no daemon running → stop is a no-op success")
}

// TestResolveDaemonPID_FallbackToLock verifies the PID is read from
// daemon.lock when daemon.pid is absent — the recovery path for a daemon
// launched directly (RunDaemon writes the PID into the lock via
// acquireDaemonLock).
func TestResolveDaemonPID_FallbackToLock(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "daemon.pid")
	lockFile := filepath.Join(t.TempDir(), "daemon.lock")

	require.NoError(t, os.WriteFile(lockFile, []byte("4242\n"), 0o644))

	pid := resolveDaemonPID(pidFile, lockFile)
	assert.Equal(t, 4242, pid, "must fall back to daemon.lock when daemon.pid is missing")
}

// TestResolveDaemonPID_PidFilePreferred verifies daemon.pid wins when both
// files exist — it is the canonical record and must take precedence over the
// lock file (which may lag if a new daemon is mid-start).
func TestResolveDaemonPID_PidFilePreferred(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "daemon.pid")
	lockFile := filepath.Join(dir, "daemon.lock")

	require.NoError(t, os.WriteFile(pidFile, []byte("1111\n"), 0o644))
	require.NoError(t, os.WriteFile(lockFile, []byte("2222\n"), 0o644))

	pid := resolveDaemonPID(pidFile, lockFile)
	assert.Equal(t, 1111, pid, "daemon.pid is canonical and must win over daemon.lock")
}

// TestResolveDaemonPID_NoFilesReturnsZero verifies that with neither file
// present, the resolver returns 0 (meaning "no PID known"), which is what
// drives StopDaemon's socket-probe branch.
func TestResolveDaemonPID_NoFilesReturnsZero(t *testing.T) {
	dir := t.TempDir()
	pid := resolveDaemonPID(
		filepath.Join(dir, "daemon.pid"),
		filepath.Join(dir, "daemon.lock"),
	)
	assert.Equal(t, 0, pid, "no PID files → 0 (unknown)")
}

// TestResolveDaemonPID_GarbageFilesReturnZero verifies malformed PID files do
// not produce a bogus PID (e.g. 0 misread as alive). A garbage file is treated
// as "no PID known" so StopDaemon falls through to the socket probe.
func TestResolveDaemonPID_GarbageFilesReturnZero(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "daemon.pid")
	lockFile := filepath.Join(dir, "daemon.lock")

	require.NoError(t, os.WriteFile(pidFile, []byte("not-a-number\n"), 0o644))
	require.NoError(t, os.WriteFile(lockFile, []byte("??"), 0o644))

	pid := resolveDaemonPID(pidFile, lockFile)
	assert.Equal(t, 0, pid, "garbage PID files → 0 (unknown), not a bogus PID")
}

// TestStopDaemon_KillsLiveProcessByPID exercises the full kill path: a PID
// file points at a real, live orphaned process (a `nohup sleep`), and
// StopDaemon must terminate it via SIGTERM and confirm death. This is the
// path that proves stop actually stops — not just that it errors on the
// unkillable case.
//
// nohup makes `sleep` ignore SIGHUP, so when its parent shell exits and it is
// reparented to init (PID 1) — exactly the real daemon's lifecycle — it
// survives until StopDaemon signals it. A plain `sleep &` would die with
// SIGHUP when the shell exits, making the test a no-op (sleep never lives
// long enough for StopDaemon to kill it). init reaps the corpse after the
// kill, so pidAlive correctly reports false post-SIGTERM.
func TestStopDaemon_KillsLiveProcessByPID(t *testing.T) {
	shortHome, err := os.MkdirTemp("/tmp", "bt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(shortHome) })
	t.Setenv("HOME", shortHome)

	// Start `nohup sleep 30` orphaned: the shell backgrounds it, prints its
	// PID, and exits. nohup lets sleep survive the parent shell's exit (no
	// SIGHUP death), so it is reparented to init and stays alive — the real
	// daemon's posture. Capture the printed PID via stdout.
	cmd := exec.Command("sh", "-c", "nohup sleep 30 >/dev/null 2>&1 & echo $!")
	out, err := cmd.Output()
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	require.NoError(t, err, "could not parse orphaned sleep PID: %q", string(out))
	t.Cleanup(func() {
		// Best-effort cleanup if the test fails before StopDaemon kills it.
		if p, e := os.FindProcess(pid); e == nil {
			_ = p.Signal(syscall.SIGKILL)
		}
	})

	// Sanity: the orphaned process is actually alive (otherwise the test would
	// pass for the wrong reason — sleep never survived to be killed).
	require.True(t, pidAlive(pid), "orphaned sleep must be alive before stop")

	pidFile, err := PIDFile()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0o644))

	// No socket listener here, so ProbeSocket returns an error — StopDaemon's
	// kill path is driven by the PID file, not the socket.
	require.NoError(t, StopDaemon(), "StopDaemon must stop the live orphan via its PID file")

	// The process must be gone (SIGTERM was enough for `sleep`); init reaped it.
	assert.False(t, pidAlive(pid), "the orphaned process must be terminated and reaped")
}

// TestStopDaemon_AlreadyStoppedPath verifies the idempotent path when a PID
// file exists but its process is already dead: StopDaemon cleans up the stale
// PID file and returns nil (no error, no kill attempt on a dead PID).
func TestStopDaemon_AlreadyStoppedPath(t *testing.T) {
	shortHome, err := os.MkdirTemp("/tmp", "bt")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(shortHome) })
	t.Setenv("HOME", shortHome)

	// A PID guaranteed not to exist (a very high number, reaped by init).
	// pidAlive will probe it and report false.
	const deadPID = 999999
	pidFile, err := PIDFile()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(pidFile, []byte(strconv.Itoa(deadPID)), 0o644))

	require.NoError(t, StopDaemon(), "a dead PID + no socket → stop is a no-op success")

	_, err = os.Stat(pidFile)
	assert.True(t, os.IsNotExist(err), "the stale PID file must be cleaned up")
}
