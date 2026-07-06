package host

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSSHRunner is the test double for sshRunner. It records every call's argv
// and scripts responses by matching the leading control verb (-O check /
// -fN ... / -O exit). This lets the Ensure/Check/Stop state machine be unit-
// tested without a real ssh host or socket.
type fakeSSHRunner struct {
	calls   [][]string
	up      bool // whether `-O check` should report a live master
	startOK bool // whether `-fN -M ...` should succeed
	stopErr error // error to return from `-O exit`
}

func (f *fakeSSHRunner) Run(args ...string) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	switch {
	case len(args) >= 2 && args[0] == "-O" && args[1] == "check":
		if f.up {
			return "Master running (pid=123)", nil
		}
		return "", errMasterDown // nonzero exit, no master
	case args[0] == "-fN":
		if f.startOK {
			f.up = true
			return "", nil
		}
		return "", errMasterDown
	case len(args) >= 2 && args[0] == "-O" && args[1] == "exit":
		return "", f.stopErr
	}
	return "", nil
}

func newTestMaster(t *testing.T, alias string) sshMaster {
	t.Helper()
	orig := sshControlDir
	sshControlDir = func() (string, error) { return t.TempDir(), nil }
	t.Cleanup(func() { sshControlDir = orig })
	return newSSHMaster(alias).withRunner(&fakeSSHRunner{})
}

// TestSocketForAlias_DeterministicAndSane proves the socket path lives under
// the boulez control dir and ends with <alias>.sock — the deterministic
// property slaves rely on (same path every time, so they find the master).
func TestSocketForAlias_DeterministicAndSane(t *testing.T) {
	orig := sshControlDir
	sshControlDir = func() (string, error) { return "/tmp/.boulez/ssh", nil }
	t.Cleanup(func() { sshControlDir = orig })

	got, err := socketForAlias("dev-machine")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/.boulez/ssh/dev-machine.sock", got)
}

// TestSocketForAlias_PropagatesError proves a control-dir resolution failure
// surfaces (so SSHHost can disable muxing rather than construct a bogus path).
func TestSocketForAlias_PropagatesError(t *testing.T) {
	orig := sshControlDir
	sshControlDir = func() (string, error) { return "", assertError("boom") }
	t.Cleanup(func() { sshControlDir = orig })
	_, err := socketForAlias("h")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

// TestSanitizeAlias proves path-unsafe characters in an alias are replaced so
// the socket path stays a single safe path component.
func TestSanitizeAlias(t *testing.T) {
	assert.Equal(t, "dev-machine", sanitizeAlias("dev-machine"))
	assert.Equal(t, "user@host_2222", sanitizeAlias("user@host:2222"))
	assert.Equal(t, "a_b_c", sanitizeAlias("a/b\\c"))
	assert.Equal(t, "with_space", sanitizeAlias("with space"))
}

// TestSSHMaster_Ensure_StartsWhenDown proves Ensure starts the master
// (`ssh -fN -M -S <sock> -o ControlPersist=yes -o ConnectTimeout=10 <alias>`)
// when none is running, then registers it.
func TestSSHMaster_Ensure_StartsWhenDown(t *testing.T) {
	m := newTestMaster(t, "dev-machine")
	r := m.runner.(*fakeSSHRunner)
	r.up = false
	r.startOK = true

	require.NoError(t, m.Ensure())

	// First call was -O check (down), second was -fN -M ... start.
	require.Len(t, r.calls, 2)
	assert.Equal(t, []string{"-O", "check", "-S", m.socket, "dev-machine"}, r.calls[0])
	assert.Equal(t, "-fN", r.calls[1][0])
	assert.Equal(t, "-M", r.calls[1][1])
	assert.Equal(t, "-S", r.calls[1][2])
	assert.Equal(t, m.socket, r.calls[1][3])
	// ControlPersist=yes and ConnectTimeout must be present.
	joined := strings.Join(r.calls[1], "\x00")
	assert.Contains(t, joined, "ControlPersist=yes")
	assert.Contains(t, joined, "ConnectTimeout=10")
	assert.Equal(t, "dev-machine", r.calls[1][len(r.calls[1])-1])

	// Registered for shutdown.
	mastersMu.Lock()
	_, ok := registeredMasters["dev-machine"]
	mastersMu.Unlock()
	assert.True(t, ok, "Ensure must register the master for StopAllMasters")
}

// TestSSHMaster_Ensure_NoopWhenUp proves Ensure is idempotent: if a master is
// already running, Ensure does NOT issue a second -fN start (just the check).
func TestSSHMaster_Ensure_NoopWhenUp(t *testing.T) {
	m := newTestMaster(t, "dev-machine")
	r := m.runner.(*fakeSSHRunner)
	r.up = true

	require.NoError(t, m.Ensure())
	require.Len(t, r.calls, 1, "up master must only be checked, not restarted")
	assert.Equal(t, []string{"-O", "check", "-S", m.socket, "dev-machine"}, r.calls[0])
}

// TestSSHMaster_Ensure_PropagatesStartError proves a master-start failure
// surfaces (rather than silently leaving slaves to discover the dead master).
func TestSSHMaster_Ensure_PropagatesStartError(t *testing.T) {
	m := newTestMaster(t, "dev-machine")
	r := m.runner.(*fakeSSHRunner)
	r.up = false
	r.startOK = false

	err := m.Ensure()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ssh master ensure dev-machine")
}

// TestSSHMaster_Ensure_AdoptsExistingAndRegisters proves a daemon that finds an
// already-running master (e.g. left by a crashed prior daemon) ADOPTS it: it
// does not start a second master, but it DOES register it so StopAllMasters
// can tear it down on graceful shutdown (no orphaned masters across crashes).
func TestSSHMaster_Ensure_AdoptsExistingAndRegisters(t *testing.T) {
	mastersMu.Lock()
	registeredMasters = map[string]sshMaster{}
	mastersMu.Unlock()

	m := newTestMaster(t, "dev-machine")
	r := m.runner.(*fakeSSHRunner)
	r.up = true // a master is already running

	require.NoError(t, m.Ensure())

	// Only a check, no -fN start.
	require.Len(t, r.calls, 1)
	assert.Equal(t, "-O", r.calls[0][0])

	// But registered for shutdown (the adopt-and-own contract).
	mastersMu.Lock()
	_, ok := registeredMasters["dev-machine"]
	mastersMu.Unlock()
	assert.True(t, ok, "an adopted master must be registered for StopAllMasters")
}

// TestSSHMaster_Check reflects the runner's notion of up/down.
func TestSSHMaster_Check(t *testing.T) {
	m := newTestMaster(t, "dev-machine")
	r := m.runner.(*fakeSSHRunner)

	r.up = false
	assert.False(t, m.Check())
	r.up = true
	assert.True(t, m.Check())
}

// TestSSHMaster_Stop issues `ssh -O exit -S <sock> <alias>`.
func TestSSHMaster_Stop(t *testing.T) {
	m := newTestMaster(t, "dev-machine")
	r := m.runner.(*fakeSSHRunner)
	require.NoError(t, m.Stop())
	require.Len(t, r.calls, 1)
	assert.Equal(t, []string{"-O", "exit", "-S", m.socket, "dev-machine"}, r.calls[0])
}

// TestStopAllMasters_StopsRegistered proves StopAllMasters tears down every
// master Ensure registered, and clears the registry.
func TestStopAllMasters_StopsRegistered(t *testing.T) {
	// Reset registry for test isolation.
	mastersMu.Lock()
	registeredMasters = map[string]sshMaster{}
	mastersMu.Unlock()

	m1 := newTestMaster(t, "dev-machine")
	m2 := newTestMaster(t, "gpu-box")
	m1.runner.(*fakeSSHRunner).up = false
	m2.runner.(*fakeSSHRunner).up = false
	m1.runner.(*fakeSSHRunner).startOK = true
	m2.runner.(*fakeSSHRunner).startOK = true

	require.NoError(t, m1.Ensure())
	require.NoError(t, m2.Ensure())

	StopAllMasters()

	// Both received -O exit.
	r1 := m1.runner.(*fakeSSHRunner)
	r2 := m2.runner.(*fakeSSHRunner)
	assert.True(t, len(r1.calls) >= 1 && r1.calls[len(r1.calls)-1][1] == "exit")
	assert.True(t, len(r2.calls) >= 1 && r2.calls[len(r2.calls)-1][1] == "exit")

	// Registry cleared.
	mastersMu.Lock()
	empty := len(registeredMasters) == 0
	mastersMu.Unlock()
	assert.True(t, empty, "StopAllMasters must clear the registry")
}

// TestSSHControlArgs proves the slave-side option injection: present with a
// socket, absent without (so muxing-disabled hosts emit plain `ssh <alias>`).
func TestSSHControlArgs(t *testing.T) {
	assert.Nil(t, sshControlArgs(""), "no socket => no control args (plain one-shot)")
	got := sshControlArgs("/tmp/x.sock")
	assert.Equal(t, []string{"-o", "ControlPath=/tmp/x.sock"}, got)
}

// errMasterDown is the sentinel the fake returns for a failed ssh (-O check
// nonzero, or a failed -fN start). It only needs to be non-nil; the real ssh
// returns *exec.ExitError, but that type can't be constructed outside os/exec.
type errSentinel struct{ msg string }

func (e errSentinel) Error() string { return e.msg }

var errMasterDown = errSentinel{"no master"}

// assertError is a tiny helper returning a fixed error, used to stub
// sshControlDir's failure path.
func assertError(msg string) error { return &errString{s: msg} }

type errString struct{ s string }

func (e *errString) Error() string { return e.s }
