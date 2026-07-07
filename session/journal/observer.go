// Package journal tails a JSONL session file written incrementally by an
// agent and exposes its last complete line. Agent-agnostic: it knows nothing
// about the line's semantics (stopReason, role, etc.) — that mapping lives in
// the program adapter.
//
// The deep-module move: a narrow "last line" surface over a tailing
// implementation that handles absent files (agent not started), empty files
// (agent started but idle), and large growing files. Designed for the daemon
// poll loop — non-blocking, called on each tick.
package journal

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
)

// ErrNoSession is returned when the session directory or its JSONL file does
// not exist yet (the agent has not started or has not written anything).
var ErrNoSession = errors.New("no session file")

// Observer tails a session directory containing a single JSONL file and
// reports its last complete line. One observer per instance; safe for
// concurrent use from the daemon poll loop.
type Observer struct {
	sessionDir string
}

// NewObserver creates an observer for the given session directory. The
// directory must contain at most one .jsonl file (the agent's session).
// Pass the same path passed to the agent via --session-dir.
func NewObserver(sessionDir string) *Observer {
	return &Observer{sessionDir: sessionDir}
}

// Content returns the last complete line of the session JSONL file. Returns
// ErrNoSession if the directory or file does not exist or is empty.
func (o *Observer) Content() (string, error) {
	path, err := o.sessionFile()
	if err != nil {
		return "", err
	}
	return readLastLine(path)
}

// sessionFile finds the single .jsonl file in the session directory. If
// multiple exist (should not happen with a dedicated --session-dir), the
// first lexicographically is returned — the observer is best-effort, not a
// session picker.
func (o *Observer) sessionFile() (string, error) {
	entries, err := os.ReadDir(o.sessionDir)
	if err != nil {
		return "", ErrNoSession
	}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
			return filepath.Join(o.sessionDir, e.Name()), nil
		}
	}
	return "", ErrNoSession
}

// readLastLine reads the file and returns its last complete (newline-
// terminated) line. A partial trailing line without a newline is ignored —
// appendFileSync writes complete lines, so a missing newline means the agent
// is mid-write and the line is not yet authoritative. Using ReadString rather
// than Scanner because Scanner returns the final partial line; ReadString
// lets us distinguish complete (newline-terminated) lines from a partial tail.
func readLastLine(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", ErrNoSession
	}
	defer func() { _ = f.Close() }()

	r := bufio.NewReader(f)
	var last string
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			last = line[:len(line)-1] // complete line — keep it
		}
		if err != nil {
			break // io.EOF or read error — stop
		}
	}
	if last == "" {
		return "", ErrNoSession
	}
	return last, nil
}
