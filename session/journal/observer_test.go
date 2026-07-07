package journal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeSession creates a session dir with a single .jsonl file containing
// the given lines (each written as a complete line with a trailing newline,
// matching appendFileSync semantics).
func writeSession(t *testing.T, dir string, lines ...string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "2026-07-07T10-00-00-000Z_abc12345.jsonl")
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func TestContent_ReturnsLastLine(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir,
		`{"type":"session","id":"abc"}`,
		`{"type":"message","message":{"role":"user","content":"hi"}}`,
		`{"type":"message","message":{"role":"assistant","stopReason":"stop"}}`,
	)
	o := NewObserver(dir)
	got, err := o.Content()
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"message","message":{"role":"assistant","stopReason":"stop"}}`, got)
}

func TestContent_SingleLine(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, `{"type":"session","id":"abc"}`)
	o := NewObserver(dir)
	got, err := o.Content()
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"session","id":"abc"}`, got)
}

func TestContent_ToolUseThenStop(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir,
		`{"type":"message","message":{"role":"assistant","stopReason":"toolUse"}}`,
		`{"type":"message","message":{"role":"toolResult"}}`,
		`{"type":"message","message":{"role":"assistant","stopReason":"stop"}}`,
	)
	o := NewObserver(dir)
	got, err := o.Content()
	require.NoError(t, err)
	assert.Contains(t, got, `"stopReason":"stop"`)
}

func TestContent_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir) // no lines
	o := NewObserver(dir)
	_, err := o.Content()
	assert.ErrorIs(t, err, ErrNoSession)
}

func TestContent_NoJsonlFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("x"), 0o644))
	o := NewObserver(dir)
	_, err := o.Content()
	assert.ErrorIs(t, err, ErrNoSession)
}

func TestContent_DirDoesNotExist(t *testing.T) {
	o := NewObserver("/nonexistent/path/that/should/not/exist")
	_, err := o.Content()
	assert.ErrorIs(t, err, ErrNoSession)
}

func TestContent_PartialTrailingLineIgnored(t *testing.T) {
	// Simulate a mid-write: complete lines followed by a partial line with no
	// trailing newline. The observer must return the last COMPLETE line.
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "session.jsonl")
	complete := `{"type":"message","message":{"role":"assistant","stopReason":"stop"}}` + "\n"
	partial := `{"type":"message","message":{"role":"assistant","stopReason":"to` // no newline
	require.NoError(t, os.WriteFile(path, []byte(complete+partial), 0o644))

	o := NewObserver(dir)
	got, err := o.Content()
	require.NoError(t, err)
	assert.Contains(t, got, `"stopReason":"stop"`)
	assert.NotContains(t, got, `"stopReason":"to`)
}

func TestContent_LargeLine(t *testing.T) {
	dir := t.TempDir()
	big := `{"type":"message","message":{"role":"assistant","content":"` +
		string(make([]byte, 100_000)) + `","stopReason":"stop"}}`
	writeSession(t, dir, `{"type":"session"}`, big)
	o := NewObserver(dir)
	got, err := o.Content()
	require.NoError(t, err)
	assert.Contains(t, got, `"stopReason":"stop"`)
}

// Ensure ErrNoSession is comparable and usable with errors.Is.
func TestErrNoSession_IsSentinel(t *testing.T) {
	assert.True(t, errors.Is(ErrNoSession, ErrNoSession))
}
