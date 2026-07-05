package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setConfigDir points the boulez config dir at a temp dir for the duration of a
// test, so ProbeSocket/ReadPID resolve against an isolated layout and never
// touch the user's real ~/.boulez. GetConfigDir reads $HOME via os.UserHomeDir on
// every call (no caching), so t.Setenv alone is sufficient.
func setConfigDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
}

// TestProbeSocket_NoSocket proves the liveness probe reports not-reachable
// when no daemon has ever bound the socket. This is the precondition the
// TUI's fail-loud contract (C1.3) relies on: an absent socket => the TUI must
// attempt a launch and fail loud if it does not come up.
func TestProbeSocket_NoSocket(t *testing.T) {
	setConfigDir(t, t.TempDir())

	err := ProbeSocket()
	require.Error(t, err, "no socket => probe must fail")
	assert.Contains(t, err.Error(), "is the daemon running?")
}

// TestProbeSocket_StaleSocketFileNotServing proves a non-socket file (or a
// socket left behind by a crashed daemon) fails the dial probe. This is the
// race the dial probe exists to catch — a stat-only check would wrongly
// report "running".
func TestProbeSocket_StaleSocketFileNotServing(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)

	socketPath := filepath.Join(dir, ".boulez", "ctl.sock")
	require.NoError(t, os.MkdirAll(filepath.Dir(socketPath), 0o755))
	// A regular file (not a listening socket) — dial must fail.
	require.NoError(t, os.WriteFile(socketPath, []byte{}, 0644))

	err := probeSocket(socketPath)
	require.Error(t, err, "a non-socket file at the socket path must fail the dial probe")
}

// TestReadPID_NoFile proves a missing PID file is not an error: a fresh
// install (or a cleanly-stopped daemon) has no PID to report.
func TestReadPID_NoFile(t *testing.T) {
	setConfigDir(t, t.TempDir())

	pid, exists, err := ReadPID()
	require.NoError(t, err)
	assert.False(t, exists, "no PID file => exists=false")
	assert.Zero(t, pid)
}

// TestReadPID_ParsesPID proves we read the PID the launcher wrote. The format
// is a bare decimal integer (written by LaunchDaemon).
func TestReadPID_ParsesPID(t *testing.T) {
	dir := t.TempDir()
	setConfigDir(t, dir)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".boulez"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".boulez", "daemon.pid"),
		[]byte("4242"), 0644))

	pid, exists, err := ReadPID()
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, 4242, pid)
}
