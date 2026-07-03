package daemon

import (
	cmd2 "claude-squad/cmd"
	"claude-squad/log"
	"claude-squad/session/tmux"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain initializes the logger before any daemon tests run. Several daemon
// helpers (e.g. reclaimOrphanedOrchestratorSession) log via the package-level
// loggers which are nil until Initialize is called; without this they panic.
func TestMain(m *testing.M) {
	log.Initialize(false)
	defer log.Close()
	os.Exit(m.Run())
}

// TestAcquireLaunchLock_FirstCallerWins proves the concurrency core of the
// auto-launch fix: when N goroutines race to acquire the launch lock (the
// scenario from dogfooding — a storm of `cs2 ctl` calls each launching a
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

// TestReclaimOrphanedOrchestratorSession_KillsLeftover proves the optional
// robustness fix: if a previous cs2 run crashed after spawning the global
// orchestrator's tmux session but before the kernel persisted the instance,
// a leftover claudesquad_orchestrator session would survive. On the next
// Ensure, the fresh spawn would collide ("tmux session already exists"). The
// reclaim step kills the orphan before the spawn so the bootstrap succeeds.
//
// This is the belt-and-suspenders fix; the root-cause fix (the daemon no
// longer clobbers the kernel's persisted state) is pinned by the kernel
// LiveInstances test. Both together make the orchestrator robust.
func TestReclaimOrphanedOrchestratorSession_KillsLeftover(t *testing.T) {
	c := cmd2.MakeExecutor()
	name := tmux.SessionName(orchestratorSessionTitle)
	// Clean slate (a previous test in this process may have left it).
	_ = tmux.KillSession(c, name)

	// Simulate an orphaned session from a crashed previous run.
	require.NoError(t, c.Run(exec.Command("tmux", "new-session", "-d", "-s", name, "sh")))
	require.True(t, tmux.SessionExists(c, name), "orphan session exists before reclaim")
	t.Cleanup(func() { _ = tmux.KillSession(c, name) })

	reclaimOrphanedOrchestratorSession()

	assert.False(t, tmux.SessionExists(c, name),
		"the orphaned orchestrator session must be killed before a fresh spawn")
}

// TestReclaimOrphanedOrchestratorSession_NoopWhenAbsent proves the reclaim is
// a safe no-op when there is nothing to reclaim (the normal path).
func TestReclaimOrphanedOrchestratorSession_NoopWhenAbsent(t *testing.T) {
	c := cmd2.MakeExecutor()
	name := tmux.SessionName(orchestratorSessionTitle)
	_ = tmux.KillSession(c, name)
	require.False(t, tmux.SessionExists(c, name))

	// Must not error and must not create a session.
	reclaimOrphanedOrchestratorSession()
	assert.False(t, tmux.SessionExists(c, name))
}
