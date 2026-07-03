package session

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInstance_ID_AllocatedAtCreation proves a freshly created instance has a
// non-empty, immutable ID — the universal handle of the control API. Callers
// that leave InstanceOptions.ID empty get an auto-allocated UUID v4.
func TestInstance_ID_AllocatedAtCreation(t *testing.T) {
	repoPath := makeTempGitRepo(t)

	inst, err := NewInstance(InstanceOptions{
		Title:   "t",
		Path:    repoPath,
		Program: "claude",
	})
	require.NoError(t, err)

	assert.NotEmpty(t, inst.ID, "NewInstance must allocate an ID")
	assert.Equal(t, inst.ID, inst.GetID(), "GetID returns the ID handle")
	// Sanity: the allocated ID looks like a hyphenated UUID v4.
	assert.Len(t, inst.ID, 36, "UUID v4 string is 36 chars")
	assert.Equal(t, "4", string(inst.ID[14]), "version nibble is 4 (v4)")
}

// TestInstance_ID_StableAcrossRoundTrip proves the ID survives a
// save→load cycle: ToInstanceData serializes it, FromInstanceData restores
// the same value. This is the persistence contract an orchestrator relies on
// to address instances by ID across a cs2 restart.
func TestInstance_ID_StableAcrossRoundTrip(t *testing.T) {
	repoPath := makeTempGitRepo(t)

	inst, err := NewInstance(InstanceOptions{
		Title:   "roundtrip",
		Path:    repoPath,
		Program: "claude",
	})
	require.NoError(t, err)

	data := inst.ToInstanceData()
	assert.Equal(t, inst.ID, data.ID, "ToInstanceData must serialize the ID")

	restored, err := FromInstanceData(data)
	require.NoError(t, err)
	assert.Equal(t, inst.ID, restored.ID, "ID must be identical after round-trip")
}

// TestInstance_ID_BackfilledForLegacyData proves backward compatibility: an
// InstanceData persisted before the ID field existed (empty ID) is backfilled
// with a fresh stable ID on load, so legacy instances become addressable by
// the control API without re-creation.
func TestInstance_ID_BackfilledForLegacyData(t *testing.T) {
	repoPath := makeTempGitRepo(t)

	// Simulate legacy persisted data: no ID field.
	legacy := InstanceData{
		Title:   "legacy",
		Path:    repoPath,
		Branch:  "cs2/legacy",
		Status:  Paused, // Paused so FromInstanceData doesn't call Start()
		Program: "claude",
		Worktree: GitWorktreeData{
			RepoPath:      repoPath,
			WorktreePath:  filepath.Join(t.TempDir(), "wt"),
			SessionName:   "legacy",
			BranchName:    "cs2/legacy",
			BaseCommitSHA: "HEAD",
		},
	}
	assert.Empty(t, legacy.ID, "precondition: legacy data has no ID")

	restored, err := FromInstanceData(legacy)
	require.NoError(t, err)
	assert.NotEmpty(t, restored.ID, "legacy instance must be backfilled with an ID")

	// The backfilled ID is itself stable across a second round-trip.
	data2 := restored.ToInstanceData()
	assert.Equal(t, restored.ID, data2.ID, "backfilled ID must persist on re-save")
	restored2, err := FromInstanceData(data2)
	require.NoError(t, err)
	assert.Equal(t, restored.ID, restored2.ID, "backfilled ID must survive a second round-trip")
}

// TestInstance_ID_Unique proves two independently created instances get
// distinct IDs (no collision / no shared counter state).
func TestInstance_ID_Unique(t *testing.T) {
	repoPath := makeTempGitRepo(t)

	a, err := NewInstance(InstanceOptions{Title: "a", Path: repoPath, Program: "claude"})
	require.NoError(t, err)
	b, err := NewInstance(InstanceOptions{Title: "b", Path: repoPath, Program: "claude"})
	require.NoError(t, err)

	assert.NotEqual(t, a.ID, b.ID, "two new instances must have distinct IDs")
}

// TestInstance_ID_PresetViaOptions proves a caller (tests, migration paths)
// can preset the ID via InstanceOptions.ID, and it is honored verbatim.
func TestInstance_ID_PresetViaOptions(t *testing.T) {
	repoPath := makeTempGitRepo(t)

	const preset = "preset-id-123"
	inst, err := NewInstance(InstanceOptions{Title: "t", Path: repoPath, Program: "claude", ID: preset})
	require.NoError(t, err)
	assert.Equal(t, preset, inst.ID, "preset ID must be honored, not overwritten")
}

// TestNewInstanceID_Format pins the UUID v4 format produced by newInstanceID:
// 36 chars, 4 hyphens, version nibble '4', variant nibble in {8,9,a,b}.
func TestNewInstanceID_Format(t *testing.T) {
	id, err := newInstanceID()
	require.NoError(t, err)

	assert.Len(t, id, 36)
	assert.Equal(t, 4, strings.Count(id, "-"), "UUID has 4 hyphens")
	assert.Equal(t, "4", string(id[14]), "version nibble at index 14 is 4")
	variant := string(id[19])
	assert.Contains(t, []string{"8", "9", "a", "b"}, variant, "variant nibble at index 19 is 10xx")
}
