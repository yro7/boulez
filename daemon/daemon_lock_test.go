package daemon

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAcquireDaemonLock_SecondCallerRefused is the core of the
// #daemon ∈ {0,1} invariant: once one holder has the daemon lock, a second
// acquireDaemonLock must FAIL immediately (not block, not succeed). This is
// what makes `boulez daemon run` safe no matter who invokes it — auto-launch,
// manual, or a future OS service: the second invocation exits cleanly at
// startup instead of double-binding the socket.
func TestAcquireDaemonLock_SecondCallerRefused(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "daemon.lock")

	release1, err := acquireDaemonLock(lock)
	require.NoError(t, err, "first caller must acquire")
	defer release1()

	// Second caller on the SAME lock file must be refused (LOCK_NB).
	_, err = acquireDaemonLock(lock)
	require.Error(t, err, "a second daemon must be refused while one is running")
	assert.Contains(t, err.Error(), "already running",
		"the refusal must be diagnosable as 'another daemon is already running'")
}

// TestAcquireDaemonLock_ReleasedOnExit proves the lock is released when the
// holder calls release — so a stopped daemon lets the next one start. This is
// the counterpart to "second caller refused": after release, a new caller
// wins. Together they pin the {0,1} invariant: at most one while alive, a new
// one allowed once the previous released.
func TestAcquireDaemonLock_ReleasedOnExit(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "daemon.lock")

	release1, err := acquireDaemonLock(lock)
	require.NoError(t, err)
	release1() // daemon stopped

	// A new caller can now acquire.
	release2, err := acquireDaemonLock(lock)
	require.NoError(t, err, "after release, a new daemon must be able to start")
	defer release2()
}

// TestAcquireDaemonLock_WritesPID proves the lock file carries the holder's
// PID (for diagnostics + StopDaemon). Not strictly required by the invariant
// (the flock is the invariant, not the file contents), but the PID file is
// what StopDaemon reads to send the kill.
func TestAcquireDaemonLock_WritesPID(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "daemon.lock")
	release, err := acquireDaemonLock(lock)
	require.NoError(t, err)
	defer release()

	data, err := os.ReadFile(lock)
	require.NoError(t, err)
	assert.Contains(t, string(data), itoa(os.Getpid()),
		"lock file must carry the holder's PID for StopDaemon")
}

// TestAcquireDaemonLock_ConcurrentCallersExactlyOneWinner is the race guard
// mirroring TestAcquireLaunchLock_FirstCallerWins: N goroutines racing to
// start a daemon — exactly ONE wins, the rest are refused. (LaunchDaemon's
// O_EXCL dedup is best-effort; this flock inside RunDaemon is the hard
// invariant, so this is the test that actually matters for #daemon ∈ {0,1}.)
func TestAcquireDaemonLock_ConcurrentCallersExactlyOneWinner(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "daemon.lock")

	const N = 10
	var mu sync.Mutex
	var winners int
	var firstRelease func()
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := acquireDaemonLock(lock)
			if err != nil {
				return // refused — the invariant holds
			}
			mu.Lock()
			winners++
			if winners == 1 {
				firstRelease = rel // keep the winner's lock alive until assertions
			} else {
				rel() // shouldn't happen, but release if it somehow did
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, winners, "exactly one concurrent caller must win the daemon lock")
	if firstRelease != nil {
		firstRelease()
	}
}
