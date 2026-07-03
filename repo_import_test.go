package main

import (
	"path/filepath"
	"testing"

	"claude-squad/ideimport"
	"claude-squad/repo"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFormatDryRunSummary covers the pure formatting + new/known counting
// used by --dry-run. The cobra glue itself is not tested (D20); the
// underlying --ide validation is covered by ideimport's tests.
func TestFormatDryRunSummary(t *testing.T) {
	reg := repo.NewRegistryAt(filepath.Join(t.TempDir(), "repos.json"))

	known := "/Users/u/projets/known"
	require.NoError(t, reg.Add(known))

	found := []ideimport.FoundRepo{
		{Path: known, IDE: "Cursor"},
		{Path: "/Users/u/projets/new1", IDE: "Antigravity"},
		{Path: "/Users/u/projets/new2", IDE: "VS Code"},
	}

	got := formatDryRunSummary(found, reg)

	assert.Contains(t, got, "Would import 3 repos (2 new, 1 already known):")
	assert.Contains(t, got, "[Cursor] "+known)
	assert.Contains(t, got, "[Antigravity] /Users/u/projets/new1")
	assert.Contains(t, got, "[VS Code] /Users/u/projets/new2")
}
