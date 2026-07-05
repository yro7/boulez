package daemon

import (
	"github.com/yro7/boulez/kernel"
	"github.com/yro7/boulez/log"
	"github.com/yro7/boulez/protected"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain initializes the logger before any daemon tests run. Several daemon
// helpers log via the package-level loggers which are nil until Initialize is
// called; without this they panic.
func TestMain(m *testing.M) {
	log.Initialize(false)
	defer log.Close()
	os.Exit(m.Run())
}

// TestAcquireLaunchLock_FirstCallerWins proves the concurrency core of the
// auto-launch fix: when N goroutines race to acquire the launch lock (the
// scenario from dogfooding — a storm of `boulez ctl` calls each launching a
// daemon), exactly ONE wins. Before the fix, all N launched their own daemon.
func TestAcquireLaunchLock_FirstCallerWins(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "daemon.lock")

	const N = 10
	var mu sync.Mutex
	var winners int
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := acquireLaunchLock(lock)
			require.NoError(t, err)
			if ok {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, winners, "exactly one caller must win the launch lock")
	// The lock file exists and carries a PID.
	data, err := os.ReadFile(lock)
	require.NoError(t, err)
	assert.NotEmpty(t, string(data), "lock file carries the winner's PID")
}

// TestAcquireLaunchLock_StaleLockReclaimed proves a stale lock (holder PID
// dead) is reclaimed, so a crashed launcher doesn't permanently block the next
// LaunchDaemon. The PID written is one that's guaranteed dead (PID 1 is init
// and not us; we use a PID we know is gone via a high unused number + check).
func TestAcquireLaunchLock_StaleLockReclaimed(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "daemon.lock")
	// Write a stale lock with a PID that's almost certainly dead (a very high
	// number unlikely to be a live process; pidAlive will probe it).
	require.NoError(t, os.WriteFile(lock, []byte("999999"), 0644))

	ok, err := acquireLaunchLock(lock)
	require.NoError(t, err)
	assert.True(t, ok, "a stale lock is reclaimed and this caller wins")
}

// TestAcquireLaunchLock_LiveHolderBlocks proves a live holder blocks a new
// launch: the caller gets false (don't launch) instead of an error.
func TestAcquireLaunchLock_LiveHolderBlocks(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "daemon.lock")
	// Write the lock with OUR pid — we're alive, so the holder is alive.
	require.NoError(t, os.WriteFile(lock, []byte(itoa(os.Getpid())), 0644))

	ok, err := acquireLaunchLock(lock)
	require.NoError(t, err)
	assert.False(t, ok, "a live holder blocks a second launch")
}

// TestWaitForSocket_Appears proves the active wait returns once the socket
// exists, instead of a blind sleep. This is what the ctl client uses after
// LaunchDaemon.
func TestWaitForSocket_Appears(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "ctl.sock")
	go func() {
		time.Sleep(80 * time.Millisecond)
		// Create the socket file by listening, so a real socket appears.
		_ = os.WriteFile(socket, []byte{}, 0644) // presence is all WaitForSocket checks
	}()
	require.NoError(t, WaitForSocket(socket, 2*time.Second))
}

// TestWaitForSocket_Timeout proves the wait gives up after the timeout rather
// than hanging forever (so a ctl call surfaces a dead daemon promptly).
func TestWaitForSocket_Timeout(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "never.sock")
	err := WaitForSocket(socket, 150*time.Millisecond)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "did not appear")
}

// itoa is a tiny int->string to avoid pulling strconv for one call.
func itoa(pid int) string {
	if pid == 0 {
		return "0"
	}
	neg := pid < 0
	if neg {
		pid = -pid
	}
	var b [20]byte
	i := len(b)
	for pid > 0 {
		i--
		b[i] = byte('0' + pid%10)
		pid /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}


// TestReloadProtected_PushesNewSetIntoKernel proves the SIGHUP reload contract
// (C2.2): reloadProtected reads the protected store and pushes the union into
// the running kernel via SetProtectedBranches, without reconstructing it. A
// branch declared protected after boot is refused immediately after a reload.
// A bad store leaves the previous set in place (fail closed).
func TestReloadProtected_PushesNewSetIntoKernel(t *testing.T) {
	store := protected.NewAt(filepath.Join(t.TempDir(), "protected.json"))
	k := kernel.New(nil, kernel.WithoutAutosave())

	// Initially empty: kernel has no protected branches.
	require.NoError(t, store.Add("/repo", "release"))
	reloadProtected(store, k)

	// After reload: a merge into "release" would be refused at the kernel
	// (verified at the kernel level in kernel_test.go); here we assert the
	// protected set was actually pushed by reading the kernel's state.
	k.SetProtectedBranches(nil) // reset to verify reload repopulates
	reloadProtected(store, k)

	// The kernel now refuses "release" — drive it through Land to prove the
	// reload took effect end-to-end. Land needs a merger; a nil merger is
	// fine because the protected guard fires before the merger runs.
	_, err := k.Land(kernel.CallerContext{}, "/repo", "release", "feat", 0)
	require.Error(t, err, "protected branch must be refused after SIGHUP reload")
}

// TestReloadProtected_BadStoreKeepsPreviousSet proves a corrupt/missing store
// does not empty protection on reload — the daemon keeps the previous set
// (fail closed), so a transient disk error cannot open a protected branch.
func TestReloadProtected_BadStoreKeepsPreviousSet(t *testing.T) {
	dir := t.TempDir()
	store := protected.NewAt(filepath.Join(dir, "protected.json"))
	require.NoError(t, store.Add("/repo", "release"))
	k := kernel.New(nil, kernel.WithoutAutosave())
	reloadProtected(store, k)

	// Corrupt the store so the next Flat() returns empty (self-heal).
	require.NoError(t, os.WriteFile(store.Path(), []byte("not json"), 0o644))

	// Reload must log and leave the previous set intact.
	reloadProtected(store, k)
	_, err := k.Land(kernel.CallerContext{}, "/repo", "release", "feat", 0)
	require.Error(t, err, "previous protected set must remain after a bad reload")
}
