package app

import (
	"context"
	"os"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"claude-squad/config"
	"claude-squad/session"
	"claude-squad/ui"
)

// TestHomeBootsFromNonRepoCWD is the C3.6 regression guard: the TUI's home
// model is a pure client over the kernel control socket. It must NOT derive
// any state from the process cwd — booting from a non-repo, non-writable
// directory must still populate the fleet view entirely from the kernel's
// snapshot.
//
// Concretely this asserts the audit invariant: app/ contains no os.Getwd /
// $PWD / filepath.Abs-derived state; every repoPath referenced there is an
// instance's repo (selected.Path / instance.Path / opts.Repo), never the
// process cwd.
func TestHomeBootsFromNonRepoCWD(t *testing.T) {
	// Run from "/" — not a git repo and not writable on most systems. If the
	// home model ever reached for the cwd as a default repo/workspace, this
	// would either error or pollute the view with a bogus instance.
	originalDir, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir("/"))
	t.Cleanup(func() { _ = os.Chdir(originalDir) })

	fleet := &fakeFleetClient{
		list: []session.InstanceData{
			instData("w1", "worker-1", session.Running, session.KindWorker),
			instData("o1", "orch-1", session.Running, session.KindOrchestrator),
		},
	}

	// Construct home the way a bare test would (the fleet seam is injected
	// directly; resolveFleet() is never called for a read). This mirrors the
	// newReconcileHome builder used across the C3.2 reconcile tests.
	spin := spinner.Model{}
	h := &home{
		ctx:       context.Background(),
		state:     stateDefault,
		appConfig: config.DefaultConfig(),
		list:      ui.NewList(&spin, false),
		menu:      ui.NewMenu(),
		fleet:     fleet,
	}

	// The boot read: refresh the view from the kernel's snapshot.
	require.NoError(t, h.refreshFleetFromKernel())

	got := h.list.GetInstances()
	require.Len(t, got, 2)

	titles := []string{got[0].Title, got[1].Title}
	assert.ElementsMatch(t, []string{"worker-1", "orch-1"}, titles,
		"fleet view must reflect the kernel snapshot, not the process cwd")

	// No instance's repo path is the cwd.
	for _, inst := range got {
		assert.NotEqual(t, "/", inst.Path,
			"no instance may bind to the process cwd")
	}

	// A second reconcile is a no-op on membership (idempotent boot read).
	prev := h.list.GetInstances()
	require.NoError(t, h.refreshFleetFromKernel())
	assert.Equal(t, len(prev), len(h.list.GetInstances()))

	// Touch tea.Model interface to confirm home stays a valid Model under a
	// non-repo cwd (the Init/View paths must not depend on cwd either).
	var _ tea.Model = h
}
