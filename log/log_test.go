package log

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestClose_SilentByDefault proves the foundational property for machine
// consumers: Close() writes NOTHING to stdout by default. `cs2 ctl` relies on
// this — its stdout must be pure JSON (a single document) so consumers can
// parse it without stripping a trailing "wrote logs to ..." line.
func TestClose_SilentByDefault(t *testing.T) {
	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })

	Initialize(false)
	Close()

	_ = w.Close()
	out, _ := io.ReadAll(r)
	os.Stdout = old // restore before assertions so failures print

	assert.Empty(t, out, "Close() must not print to stdout by default (ctl stdout stays pure JSON)")
}

// TestClose_PrintsPathWhenEnabled proves the human-facing behaviour: when
// SetPrintPathOnClose(true) is active, Close prints the log file path line.
// The interactive TUI / reset / debug commands use this so a human knows
// where the logs went.
func TestClose_PrintsPathWhenEnabled(t *testing.T) {
	var buf bytes.Buffer
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	prev := SetPrintPathOnClose(true)
	t.Cleanup(func() { SetPrintPathOnClose(prev); os.Stdout = old })

	Initialize(false)
	Close()

	_ = w.Close()
	_, _ = io.Copy(&buf, r)
	os.Stdout = old

	assert.Contains(t, buf.String(), "wrote logs to", "Close prints the log path when enabled")
	assert.Contains(t, buf.String(), LogFilePath(), "mentions the actual log file path")
}
