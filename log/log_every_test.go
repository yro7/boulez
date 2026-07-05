package log

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestEvery_FirstCallShouldLog proves the rate limiter fires on the first
// ShouldLog call regardless of the configured timeout. The status-polling loop
// in the daemon relies on the first tick being immediate (not waiting a full
// timeout before the first log).
func TestEvery_FirstCallShouldLog(t *testing.T) {
	e := NewEvery(time.Hour) // long timeout so subsequent calls are suppressed
	assert.True(t, e.ShouldLog(), "first call must always fire")
}

// TestEvery_SuppressesWithinTimeout proves that once fired, subsequent calls
// within the timeout are suppressed (returns false). This is the
// spam-prevention contract: a hot loop calling ShouldLog does not flood the log.
func TestEvery_SuppressesWithinTimeout(t *testing.T) {
	e := NewEvery(time.Hour)
	assert.True(t, e.ShouldLog())
	assert.False(t, e.ShouldLog(), "second call within timeout must be suppressed")
	assert.False(t, e.ShouldLog())
}

// TestEvery_FiresAgainAfterTimeout proves the limiter re-arms: after the
// timeout elapses, ShouldLog returns true again. This is the "log at most
// once per N" contract used by the daemon's periodic status logger.
func TestEvery_FiresAgainAfterTimeout(t *testing.T) {
	e := NewEvery(20 * time.Millisecond)
	assert.True(t, e.ShouldLog())
	assert.False(t, e.ShouldLog())

	// Wait for the timer to fire, then the next call must re-arm.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if e.ShouldLog() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("ShouldLog never returned true after the timeout elapsed")
}

// TestEvery_ZeroTimeoutStillFiresOnce proves a zero (or near-zero) timeout
// does not crash and still allows the first call through. Guards against a
// divide-by-zero / nil-timer panic in a misconfiguration.
func TestEvery_ZeroTimeoutStillFiresOnce(t *testing.T) {
	e := NewEvery(0)
	// First call must fire without panicking.
	assert.NotPanics(t, func() {
		e.ShouldLog()
	})
}

// TestSetPrintPathOnClose_ReturnsPrevious proves the toggle is idempotent
// and restorable: SetPrintPathOnClose returns the previous value so callers
// can save/restore it around a command (used by ctl vs human commands).
func TestSetPrintPathOnClose_ReturnsPrevious(t *testing.T) {
	prev := SetPrintPathOnClose(true)
	t.Cleanup(func() { SetPrintPathOnClose(prev) })

	// Toggling returns the value that was in effect before the toggle.
	assert.True(t, SetPrintPathOnClose(false), "must return previous=true")
	assert.False(t, SetPrintPathOnClose(true), "must return previous=false")
}

// TestLogFilePath_NonEmpty proves the log file path is exposed for
// human-facing commands that want to mention it without the Close side
// effect (polluting stdout). It must be a non-empty string after Initialize.
func TestLogFilePath_NonEmpty(t *testing.T) {
	Initialize(false)
	t.Cleanup(Close)
	assert.NotEmpty(t, LogFilePath(), "log file path must be exposed and non-empty")
}

// TestInitialize_DaemonPrefix proves the daemon-mode loggers carry the
// [DAEMON] prefix so log entries from the daemon are distinguishable from a
// one-shot command in the same log file. Guards against a regression that
// would drop the prefix (and thus the ability to grep daemon lines).
func TestInitialize_DaemonPrefix(t *testing.T) {
	Initialize(true)
	t.Cleanup(Close)

	// The daemon InfoLog prefix must contain the [DAEMON] marker.
	assert.Contains(t, InfoLog.Prefix(), "[DAEMON]")
	assert.Contains(t, WarningLog.Prefix(), "[DAEMON]")
	assert.Contains(t, ErrorLog.Prefix(), "[DAEMON]")
}

// TestInitialize_NonDaemonNoPrefix proves the non-daemon loggers do NOT carry
// the [DAEMON] prefix (so a regression that always adds the prefix is caught).
func TestInitialize_NonDaemonNoPrefix(t *testing.T) {
	Initialize(false)
	t.Cleanup(Close)

	assert.NotContains(t, InfoLog.Prefix(), "[DAEMON]")
	assert.NotContains(t, WarningLog.Prefix(), "[DAEMON]")
	assert.NotContains(t, ErrorLog.Prefix(), "[DAEMON]")
}
