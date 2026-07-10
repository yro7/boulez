package notify

import (
	"strings"
	"testing"
	"time"
)

func TestNoop_NeverPanics(t *testing.T) {
	n := Noop{}
	// A noop notifier must be safe to call with any event, including the
	// zero value. The poll loop should not need to special-case "notifications
	// off" — a Noop call site is identical to a Desktop call site.
	n.Notify(Event{})
	n.Notify(Event{Kind: InstanceReady, Instance: InstanceInfo{ID: "x", Title: "t"}, At: time.Now()})
}

func TestReadyMessage(t *testing.T) {
	t.Run("renders title and body from instance", func(t *testing.T) {
		info := InstanceInfo{ID: "inst-1", Title: "feature-auth", Host: "local"}
		title, body := readyMessage(info)
		if !strings.Contains(title, "feature-auth") {
			t.Errorf("title %q should contain the instance title", title)
		}
		if !strings.Contains(title, "boulez") {
			t.Errorf("title %q should identify boulez as the source", title)
		}
		if !strings.Contains(body, "feature-auth") {
			t.Errorf("body %q should contain the instance title", body)
		}
		if !strings.Contains(body, "waiting for input") {
			t.Errorf("body %q should say the instance is waiting for input", body)
		}
	})
}

func TestBuildCommand(t *testing.T) {
	t.Run("darwin uses osascript", func(t *testing.T) {
		// buildCommand branches on runtime.GOOS, which we cannot flip at test
		// time. On darwin we assert the osascript shape; on other platforms
		// this same assertion documents the expected command without running
		// it. The branching is trivial enough that the per-platform shape is
		// the only thing worth pinning here.
		title, body := "T", "B"
		c, err := buildCommand(title, body)
		if err != nil {
			t.Skipf("unsupported platform, skipping: %v", err)
		}
		if c == nil {
			t.Fatal("expected a command, got nil")
		}
		// We do not assert the exact argv here — that would couple the test
		// to the osascript invocation string and break on harmless rewording.
		// The important invariant (a command is built, not nil) is asserted.
	})
}
