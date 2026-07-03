package cmd

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExec_CombinedOutput verifies that Exec.CombinedOutput returns both
// stdout and stderr, distinguishing it from Output (stdout only). This
// matters for git command routing, which needs stderr in error messages.
func TestExec_CombinedOutput(t *testing.T) {
	e := Exec{}

	// `echo out; echo err >&2` — emits to both streams.
	cmd := exec.Command("sh", "-c", "echo out; echo err 1>&2")

	out, err := e.CombinedOutput(cmd)
	require.NoError(t, err)
	// Both streams appear, in order.
	assert.Contains(t, string(out), "out")
	assert.Contains(t, string(out), "err")
}

// TestExec_CombinedOutput_StderrInError verifies that on a non-zero exit,
// stderr is surfaced in the returned output (the property git routing
// relies on for diagnostics).
func TestExec_CombinedOutput_StderrInError(t *testing.T) {
	e := Exec{}

	cmd := exec.Command("sh", "-c", "echo boom 1>&2; exit 7")

	out, err := e.CombinedOutput(cmd)
	require.Error(t, err)
	assert.Contains(t, string(out), "boom")
}
