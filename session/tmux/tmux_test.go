package tmux

import (
	cmd2 "claude-squad/cmd"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"claude-squad/cmd/cmd_test"

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
	require.Equal(t, fmt.Sprintf("tmux new-session -d -s claudesquad_test-session -c %s claude", workdir),
		cmd2.ToString(ptyFactory.cmds[0]))
	require.Equal(t, "tmux attach-session -t claudesquad_test-session",
		cmd2.ToString(ptyFactory.cmds[1]))

	require.Equal(t, 2, len(ptyFactory.files))

	// File should be closed.
	_, err = ptyFactory.files[0].Stat()
	require.Error(t, err)
	// File should be open
	_, err = ptyFactory.files[1].Stat()
	require.NoError(t, err)
}

// TestSessionName_Deterministic pins that SessionName is the single source of
// truth for the title→session-name mapping and matches the sanitization a
// TmuxSession applies internally. Other packages (the daemon's orchestrator
// bootstrap) rely on this to reason about a session by name without
// constructing a TmuxSession, so the two must agree.
func TestSessionName_Deterministic(t *testing.T) {
	require.Equal(t, "claudesquad_orchestrator", SessionName("orchestrator"))
	require.Equal(t, "claudesquad_orchestrator", SessionName("orchestrator"),
		"stable across calls")
	// Sanitization parity with NewTmuxSession.
	require.Equal(t,
		NewTmuxSession("a sd f. asdf", "p").sanitizedName,
		SessionName("a sd f. asdf"))
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
	name := "claudesquad_test_exists_kill"
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
