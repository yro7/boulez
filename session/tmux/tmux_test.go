package tmux

import (
	"fmt"
	cmd2 "github.com/yro7/boulez/cmd"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yro7/boulez/cmd/cmd_test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type MockPtyFactory struct {
	t *testing.T

	// Array of commands and the corresponding file handles representing PTYs.
	cmds  []*exec.Cmd
	files []*os.File
}

func (pt *MockPtyFactory) Start(cmd *exec.Cmd) (*os.File, error) {
	filePath := filepath.Join(pt.t.TempDir(), fmt.Sprintf("pty-%s-%d", pt.t.Name(), rand.Int31()))
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0644)
	if err == nil {
		pt.cmds = append(pt.cmds, cmd)
		pt.files = append(pt.files, f)
	}
	return f, err
}

func (pt *MockPtyFactory) Close() {}

func NewMockPtyFactory(t *testing.T) *MockPtyFactory {
	return &MockPtyFactory{
		t: t,
	}
}

func TestSanitizeName(t *testing.T) {
	session := NewTmuxSession("asdf", "program")
	require.Equal(t, TmuxPrefix+"asdf", session.sanitizedName)

	session = NewTmuxSession("a sd f . . asdf", "program")
	require.Equal(t, TmuxPrefix+"asdf__asdf", session.sanitizedName)
}

func TestStartTmuxSession(t *testing.T) {
	ptyFactory := NewMockPtyFactory(t)

	created := false
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			if strings.Contains(cmd.String(), "has-session") && !created {
				created = true
				return fmt.Errorf("session already exists")
			}
			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			return []byte("output"), nil
		},
	}

	workdir := t.TempDir()
	session := newTmuxSession("test-session", "claude", ptyFactory, cmdExec)

	err := session.Start(workdir)
	require.NoError(t, err)
	require.Equal(t, 2, len(ptyFactory.cmds))
	require.Equal(t, fmt.Sprintf("tmux new-session -d -s boulez_test-session -c %s claude", workdir),
		cmd2.ToString(ptyFactory.cmds[0]))
	require.Equal(t, "tmux attach-session -t boulez_test-session",
		cmd2.ToString(ptyFactory.cmds[1]))

	require.Equal(t, 2, len(ptyFactory.files))

	// File should be closed.
	_, err = ptyFactory.files[0].Stat()
	require.Error(t, err)
	// File should be open
	_, err = ptyFactory.files[1].Stat()
	require.NoError(t, err)
}

// TestClose_IdempotentWhenSessionGone proves the zombie fix: Close() on a
// session that is already gone (e.g. the instance was reconciled to Dead
// after a tmux crash) is a no-op success, not an error. Without this, killing
// an already-Dead instance failed the whole Kill and its record lingered in
// the fleet forever (the unkillable-zombie regression).
func TestClose_IdempotentWhenSessionGone(t *testing.T) {
	// Mock executor: has-session fails (session gone), kill-session also
	// fails. Pre-fix, Close() would append the kill-session error; post-fix,
	// the kill error is swallowed because DoesSessionExist() is false.
	exec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			s := cmd2.ToString(c)
			if strings.Contains(s, "has-session") {
				return fmt.Errorf("can't find session") // session gone
			}
			if strings.Contains(s, "kill-session") {
				return fmt.Errorf("kill-session failed") // should be swallowed
			}
			return nil
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return []byte{}, nil },
	}

	sess := newTmuxSession("gone-session", "bash", NewMockPtyFactory(t), exec)
	err := sess.Close()
	require.NoError(t, err, "Close on a gone session is a no-op success")
}

// TestClose_SurfacesErrorOnLiveSession proves the other side: when the session
// IS still alive but kill-session fails (a wedged session), Close() surfaces
// the error rather than silently swallowing it. This is the guard against
// making Close() too lenient — a real kill failure on a live session must
// still be reported.
func TestClose_SurfacesErrorOnLiveSession(t *testing.T) {
	exec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			s := cmd2.ToString(c)
			if strings.Contains(s, "has-session") {
				return nil // session IS alive
			}
			if strings.Contains(s, "kill-session") {
				return fmt.Errorf("kill-session wedged")
			}
			return nil
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return []byte{}, nil },
	}

	sess := newTmuxSession("wedge-session", "bash", NewMockPtyFactory(t), exec)
	err := sess.Close()
	require.Error(t, err, "kill failure on a live session is surfaced")
	assert.Contains(t, err.Error(), "kill-session")
}

// TestSessionName_Deterministic pins that SessionName is the single source of
// truth for the title→session-name mapping and matches the sanitization a
// TmuxSession applies internally. Other packages (the daemon's orchestrator
// bootstrap) rely on this to reason about a session by name without
// constructing a TmuxSession, so the two must agree.
func TestSessionName_Deterministic(t *testing.T) {
	require.Equal(t, "boulez_orchestrator", SessionName("orchestrator"))
	require.Equal(t, "boulez_orchestrator", SessionName("orchestrator"),
		"stable across calls")
	// Sanitization parity with NewTmuxSession.
	require.Equal(t,
		NewTmuxSession("a sd f. asdf", "p").sanitizedName,
		SessionName("a sd f. asdf"))
}

// TestSendKeys_TapEnter_TapDAndEnter_PinSendKeysArgv pins that SendKeys,
// TapEnter, and TapDAndEnter route through `tmux send-keys` via the host
// executor (cmdExec) rather than writing raw bytes to an attached PTY. This
// is what lets the TUI send input to an instance with no PTY open and what
// makes remote (SSH) hosts work for free — the same channel
// CapturePaneContent uses for reading. The literal flag (-l) on SendKeys
// ensures a user typing the word "Enter" is sent as letters, not the
// Enter key.
func TestSendKeys_TapEnter_TapDAndEnter_PinSendKeysArgv(t *testing.T) {
	var ran []string
	exec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			ran = append(ran, cmd2.ToString(c))
			return nil
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return []byte{}, nil },
	}
	sess := newTmuxSession("keys-test", "bash", NewMockPtyFactory(t), exec)

	require.NoError(t, sess.SendKeys("hello world"))
	require.NoError(t, sess.SendKeys("Enter")) // literal: the word, not the key
	require.NoError(t, sess.TapEnter())
	require.NoError(t, sess.TapDAndEnter())

	require.Equal(t, []string{
		"tmux send-keys -t boulez_keys-test -l hello world",
		"tmux send-keys -t boulez_keys-test -l Enter",
		"tmux send-keys -t boulez_keys-test Enter",
		"tmux send-keys -t boulez_keys-test D Enter",
	}, ran, "SendKeys/TapEnter/TapDAndEnter must use tmux send-keys via cmdExec")
}

// TestSendKey_PinSendKeyArgv pins that SendKey routes a single named key
// through `tmux send-keys` (without -l) via the host executor. This is the
// named-key counterpart to SendKeys' literal text and the channel the TUI's
// insert mode uses to forward backspace, arrows, Ctrl-C, etc. so the agent's
// own readline stays the authority over editing.
func TestSendKey_PinSendKeyArgv(t *testing.T) {
	var ran []string
	exec := cmd_test.MockCmdExec{
		RunFunc: func(c *exec.Cmd) error {
			ran = append(ran, cmd2.ToString(c))
			return nil
		},
		OutputFunc: func(c *exec.Cmd) ([]byte, error) { return []byte{}, nil },
	}
	sess := newTmuxSession("keys-test", "bash", NewMockPtyFactory(t), exec)

	require.NoError(t, sess.SendKey("BSpace"))
	require.NoError(t, sess.SendKey("C-c"))
	require.NoError(t, sess.SendKey("M-b"))

	require.Equal(t, []string{
		"tmux send-keys -t boulez_keys-test BSpace",
		"tmux send-keys -t boulez_keys-test C-c",
		"tmux send-keys -t boulez_keys-test M-b",
	}, ran, "SendKey must route a single named key via tmux send-keys (no -l)")
}

// TestSessionExists_and_KillSession proves the package-level helpers used by
// the orchestrator's orphan-reclamation: SessionExists detects a real session
// and KillSession removes it. Uses a real tmux server (these are thin
// wrappers over `tmux has-session` / `tmux kill-session`).
//
// We create the fixture session with CombinedOutput (capturing stderr) rather
// than Run because `tmux new-session` is finicky about inherited stdio under
// `go test` (stdin is /dev/null) and exits 1 with no diagnostic; this is a
// test-harness quirk, not behaviour of the helpers under test.
func TestSessionExists_and_KillSession(t *testing.T) {
	c := cmd2.MakeExecutor()
	name := "boulez_test_exists_kill"
	// Clean slate.
	_ = KillSession(c, name)
	require.False(t, SessionExists(c, name), "session must not exist before creation")

	_, err := c.CombinedOutput(exec.Command("tmux", "new-session", "-d", "-s", name, "sh"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = KillSession(c, name) })

	require.True(t, SessionExists(c, name), "session exists after creation")

	require.NoError(t, KillSession(c, name))
	require.False(t, SessionExists(c, name), "session gone after KillSession")

	// KillSession on a non-existent session: kill-session exits 1 for an
	// absent session, so KillSession surfaces that error. The orchestrator's
	// reclaim tolerates it (it checks existence first). We assert the behaviour
	// is documented: an error is returned, not a silent nil.
	require.Error(t, KillSession(c, name), "kill-session on absent session exits non-zero")
}
