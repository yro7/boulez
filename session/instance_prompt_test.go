package session

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePane is a minimal paneTyper for exercising sendPromptReliably. It
// records every SendKeys call (so a test can assert we never flood the agent
// with re-typed prompts) and decides what CapturePaneContent returns via a
// caller-supplied function (so a test can simulate "input handler swallowed
// the first keystrokes" vs "echoed immediately").
type fakePane struct {
	mu        sync.Mutex
	sentKeys  []string
	captureFn func(sent []string) string
}

func (f *fakePane) SendKeys(keys string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentKeys = append(f.sentKeys, keys)
	return nil
}

func (f *fakePane) TapEnter() error { return nil }

func (f *fakePane) CapturePaneContent() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.captureFn(f.sentKeys), nil
}

// TestSendPromptReliablyDoesNotFloodOnMultiLine is the regression test for the
// orchestrator crash: a multi-line prompt (the orchestrator injection prompt
// shape) whose full text never appears contiguously in the captured pane must
// NOT be re-typed every 100ms for 30s. Re-typing a multi-line prompt appends
// duplicate input and floods the agent, crashing it.
//
// The fix: Phase 1 re-types only while the echo MARKER (first non-empty line)
// is absent. As soon as the marker is visible the input handler is live, so we
// stop typing and proceed to submit. Here the capture echoes the marker after
// the first SendKeys, so SendKeys must be called exactly once.
func TestSendPromptReliablyDoesNotFloodOnMultiLine(t *testing.T) {
	prompt := "You are the cs2 global orchestrator.\n\nYou are supervised, not autonomous. Do the following, in order:\n1. Read ./ORCHESTRATOR.md.\n2. STOP and wait."
	marker := promptEchoMarker(prompt)
	if !strings.Contains(prompt, marker) {
		t.Fatalf("marker %q must be a substring of the prompt", marker)
	}
	if marker != "You are the cs2 global orchestrator." {
		t.Fatalf("marker = %q, want the first non-empty line", marker)
	}

	pane := &fakePane{}
	// callCount distinguishes Phase-1 captures (marker present) from the
	// Phase-2 capture (marker drained after submit). Phase 2's first TapEnter
	// precedes its first capture, so the capture after the Phase-1 echoes is
	// the submit check.
	callCount := 0
	pane.captureFn = func(sent []string) string {
		callCount++
		if len(sent) == 0 {
			return "" // boot: nothing echoed yet
		}
		if callCount <= 2 {
			// Phase 1: marker present (echoed) but the FULL multi-line prompt
			// is not contiguous (line wraps / per-row prefixes), which is what
			// defeated the old full-string check and caused the flood.
			return "prefix " + marker + " suffix (wrapped, not the full multi-line prompt)"
		}
		// Phase 2: marker gone (submitted).
		return ""
	}

	done := make(chan error, 1)
	go func() { done <- sendPromptReliably(pane, prompt) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("sendPromptReliably returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sendPromptReliably did not return within 5s (deadline loop not bounded?)")
	}

	pane.mu.Lock()
	sends := len(pane.sentKeys)
	pane.mu.Unlock()
	if sends != 1 {
		t.Fatalf("expected exactly 1 SendKeys (no flood), got %d", sends)
	}
}

// TestSendPromptReliablyRetypesWhenSwallowed verifies the other side of the
// fix: if the first SendKeys is swallowed mid-boot (marker absent), Phase 1
// DOES re-type — but only until the marker appears, then stops. This is the
// determinism benefit of the closed loop (no blind timer).
func TestSendPromptReliablyRetypesWhenSwallowed(t *testing.T) {
	prompt := "single-line task"
	marker := promptEchoMarker(prompt)
	if marker != "single-line task" {
		t.Fatalf("marker for single-line prompt = %q, want the prompt itself", marker)
	}

	pane := &fakePane{}
	calls := 0
	pane.captureFn = func(sent []string) string {
		calls++
		// Swallow the first SendKeys (marker absent), echo on the second.
		if len(sent) < 2 {
			return "" // boot: input handler not live yet
		}
		// Once live, echo the marker; after a few Phase-2 polls, drain it.
		if calls > 4 {
			return "" // submitted
		}
		return "echo: " + marker
	}

	done := make(chan error, 1)
	go func() { done <- sendPromptReliably(pane, prompt) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("sendPromptReliably returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sendPromptReliably did not return within 5s")
	}

	pane.mu.Lock()
	sends := len(pane.sentKeys)
	pane.mu.Unlock()
	if sends < 2 {
		t.Fatalf("expected at least 2 SendKeys (re-type after swallow), got %d", sends)
	}
	// And critically, no flood: a handful of re-types while booting, not 300.
	if sends > 10 {
		t.Fatalf("SendKeys called %d times — looks like a flood (marker never detected)", sends)
	}
}

func TestPromptEchoMarker(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   string
	}{
		{"single line", "do the thing", "do the thing"},
		{"multi line first non-empty", "\n\nYou are supervised.\nsecond line", "You are supervised."},
		{"trims whitespace", "  trimmed first line  \nsecond", "trimmed first line"},
		{"caps long lines", strings.Repeat("x", 60), strings.Repeat("x", 40)},
		{"empty falls back", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := promptEchoMarker(c.prompt)
			if got != c.want {
				t.Errorf("promptEchoMarker(%q) = %q, want %q", c.prompt, got, c.want)
			}
		})
	}
}
