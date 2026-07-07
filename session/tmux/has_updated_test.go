package tmux

import (
	"os/exec"
	"testing"
	"time"

	"github.com/yro7/boulez/cmd/cmd_test"
	"github.com/yro7/boulez/program"

	"github.com/stretchr/testify/assert"
)

// stubAdapter is a minimal program.Adapter for testing HasUpdated's status
// propagation without depending on any real agent's pane format.
type stubAdapter struct {
	status program.Status
}

func (s stubAdapter) Name() string        { return "stub" }
func (s stubAdapter) Matches(string) bool { return true }
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
	s := newTmuxSession("ready-test", "stub", cmdExec)
	s.adapter = stubAdapter{status: program.StatusReady}
	s.monitor = newStatusMonitor()

	// First tick: content changed -> updated=true, status=Ready.
	updated, status, stableFor := s.HasUpdated()
	assert.True(t, updated, "first tick should report content changed")
	assert.Equal(t, program.StatusReady, status, "ready status must be reported on change")
	assert.Equal(t, time.Duration(0), stableFor, "stableFor is zero on a change tick")

	// Second tick: content STABLE. The bug was that hasPrompt=true led the
	// caller to TapEnter() without ever setting Ready, so the spinner spun
	// forever. HasUpdated must still surface StatusReady here.
	updated, status, stableFor = s.HasUpdated()
	assert.False(t, updated, "second tick: content unchanged")
	assert.Equal(t, program.StatusReady, status, "ready status must survive stable content")
	assert.Greater(t, stableFor, time.Duration(0), "stableFor grows once the pane is stable")
}

// TestHasUpdated_WorkingOnUnknownAdapter verifies that for an agent we don't
// detect (StatusUnknown), the content-change heuristic still drives Running so
// unknown agents keep cycling like before the refactor.
func TestHasUpdated_WorkingOnUnknownAdapter(t *testing.T) {
	cmdExec := cmd_test.MockCmdExec{
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			return []byte("changing content"), nil
		},
	}
	s := newTmuxSession("unknown-test", "noop", cmdExec)
	s.adapter = stubAdapter{status: program.StatusUnknown}
	s.monitor = newStatusMonitor()

	updated, status, _ := s.HasUpdated()
	assert.True(t, updated)
	assert.Equal(t, program.StatusUnknown, status)

	// Stable content -> not updated, status still Unknown. The caller falls
	// back to its heuristic (no Running flip) for unknown agents.
	updated, status, _ = s.HasUpdated()
	assert.False(t, updated)
	assert.Equal(t, program.StatusUnknown, status)
}

// TestHasUpdated_StabilityFallbackForUnknownAgent is the core test for the
// agent-agnostic ready detection: an adapter that never returns StatusReady
// (e.g. an unknown harness, or a known agent whose sentinel extension isn't
// installed) must still let the caller observe that the pane has been stable
// for a long time, via stableFor. The caller (app.go reconciliation) treats a
// stableFor above threshold as Ready — so boulez works for any harness without
// a dedicated adapter, instead of leaving the instance stuck on Running
// forever (the bug that motivated this fallback).
func TestHasUpdated_StabilityFallbackForUnknownAgent(t *testing.T) {
	const paneContent = "agent finished, prompt sitting idle"
	cmdExec := cmd_test.MockCmdExec{
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			return []byte(paneContent), nil
		},
	}
	s := newTmuxSession("fallback-test", "noop", cmdExec)
	s.adapter = stubAdapter{status: program.StatusUnknown} // no ready signal ever
	s.monitor = newStatusMonitor()

	// First tick: content appears -> updated=true, stableFor=0.
	_, status, stableFor := s.HasUpdated()
	assert.Equal(t, program.StatusUnknown, status)
	assert.Equal(t, time.Duration(0), stableFor)

	// Simulate the pane having been stable for 2 minutes: rewind lastChangeAt
	// so the next tick reports a large stableFor without sleeping.
	s.monitor.lastChangeAt = time.Now().Add(-2 * time.Minute)

	updated, status, stableFor := s.HasUpdated()
	assert.False(t, updated, "content unchanged")
	assert.Equal(t, program.StatusUnknown, status, "adapter still says Unknown")
	assert.GreaterOrEqual(t, stableFor, 2*time.Minute,
		"stableFor must reflect elapsed time since last change so the caller can apply the threshold")
}

// TestHasUpdated_StabilityResetsOnChange verifies that producing output
// (pane content changes) resets the stability clock, so an agent that resumes
// streaming does not keep a stale large stableFor.
func TestHasUpdated_StabilityResetsOnChange(t *testing.T) {
	content := "first"
	cmdExec := cmd_test.MockCmdExec{
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			return []byte(content), nil
		},
	}
	s := newTmuxSession("reset-test", "noop", cmdExec)
	s.adapter = stubAdapter{status: program.StatusUnknown}
	s.monitor = newStatusMonitor()

	// First tick establishes baseline.
	s.HasUpdated()
	// Pretend it was stable a long time.
	s.monitor.lastChangeAt = time.Now().Add(-5 * time.Minute)

	// Agent emits a new line: content changes -> stableFor must reset to 0.
	content = "second: agent resumed streaming"
	updated, _, stableFor := s.HasUpdated()
	assert.True(t, updated, "content changed")
	assert.Equal(t, time.Duration(0), stableFor, "stableFor resets to 0 when the pane changes")
}
