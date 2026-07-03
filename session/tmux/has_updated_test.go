package tmux

import (
	"os/exec"
	"testing"

	"claude-squad/cmd/cmd_test"
	"claude-squad/program"

	"github.com/stretchr/testify/assert"
)

// stubAdapter is a minimal program.Adapter for testing HasUpdated's status
// propagation without depending on any real agent's pane format.
type stubAdapter struct {
	status program.Status
}

func (s stubAdapter) Name() string                                { return "stub" }
func (s stubAdapter) Matches(string) bool                         { return true }
func (s stubAdapter) Detect(string) (program.Status, *program.Prompt) {
	return s.status, nil
}

// TestHasUpdated_ReadySurvivesStableContent is a regression test for the bug
// where a finished agent (pane content stable, adapter says StatusReady) was
// reported as still working. The old HasUpdated returned a lossy hasPrompt bool
// and the caller classified a stable-but-ready pane as Running; now the
// precise program.Status is returned and the caller must honour it.
func TestHasUpdated_ReadySurvivesStableContent(t *testing.T) {
	const paneContent = "some stable pane content with a ready marker"
	cmdExec := cmd_test.MockCmdExec{
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			return []byte(paneContent), nil
		},
	}
	s := newTmuxSession("ready-test", "stub", NewMockPtyFactory(t), cmdExec)
	s.adapter = stubAdapter{status: program.StatusReady}
	s.monitor = newStatusMonitor()

	// First tick: content changed -> updated=true, status=Ready.
	updated, status := s.HasUpdated()
	assert.True(t, updated, "first tick should report content changed")
	assert.Equal(t, program.StatusReady, status, "ready status must be reported on change")

	// Second tick: content STABLE. The bug was that hasPrompt=true led the
	// caller to TapEnter() without ever setting Ready, so the spinner spun
	// forever. HasUpdated must still surface StatusReady here.
	updated, status = s.HasUpdated()
	assert.False(t, updated, "second tick: content unchanged")
	assert.Equal(t, program.StatusReady, status, "ready status must survive stable content")
}

// TestHasUpdated_WorkingOnUnknownAdapter verifies that for an agent we don't
// detect (StatusUnknown), the content-change heuristic still drives Running so
// unknown agents keep cycling like before the refactor.
func TestHasUpdated_WorkingOnUnknownAdapter(t *testing.T) {
	callCount := 0
	cmdExec := cmd_test.MockCmdExec{
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			callCount++
			return []byte("changing content"), nil // always "different" by hash? no — same string
		},
	}
	s := newTmuxSession("unknown-test", "noop", NewMockPtyFactory(t), cmdExec)
	s.adapter = stubAdapter{status: program.StatusUnknown}
	s.monitor = newStatusMonitor()

	updated, status := s.HasUpdated()
	assert.True(t, updated)
	assert.Equal(t, program.StatusUnknown, status)

	// Stable content -> not updated, status still Unknown. The caller falls
	// back to its heuristic (no Running flip) for unknown agents.
	updated, status = s.HasUpdated()
	assert.False(t, updated)
	assert.Equal(t, program.StatusUnknown, status)
}
