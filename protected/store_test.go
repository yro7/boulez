package protected

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestStore returns a Store backed by a temp file, isolated from the real
// ~/.cs2/protected.json.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewAt(filepath.Join(t.TempDir(), "protected.json"))
}

func TestStore_Load_EmptyWhenAbsent(t *testing.T) {
	s := newTestStore(t)

	got, err := s.Load()
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

func TestStore_Load_CorruptSelfHealsToEmpty(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(s.path), 0o755))
	require.NoError(t, os.WriteFile(s.path, []byte("not json"), 0o644))

	got, err := s.Load()
	require.NoError(t, err, "a corrupt store must not block the daemon")
	assert.Empty(t, got)
}

func TestStore_Add_PersistsAndResolvesAbsolute(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.Add(".", "integration"))

	got, err := s.Load()
	require.NoError(t, err)
	require.Len(t, got, 1)
	abs, err := filepath.Abs(".")
	require.NoError(t, err)
	assert.Equal(t, []string{"integration"}, got[abs])
}

func TestStore_Add_IsIdempotent(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.Add("/repo", "main"))
	require.NoError(t, s.Add("/repo", "main"))

	got, err := s.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"main"}, got["/repo"])
}

func TestStore_Add_RejectsEmptyBranch(t *testing.T) {
	s := newTestStore(t)
	require.Error(t, s.Add("/repo", ""))
}

func TestStore_Remove_UnprotectsBranch(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.Add("/repo", "main"))
	require.NoError(t, s.Add("/repo", "integration"))

	require.NoError(t, s.Remove("/repo", "main"))

	got, err := s.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"integration"}, got["/repo"])
}

func TestStore_Remove_DropsRepoWhenLastBranchRemoved(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.Add("/repo", "main"))

	require.NoError(t, s.Remove("/repo", "main"))

	got, err := s.Load()
	require.NoError(t, err)
	assert.NotContains(t, got, "/repo", "an empty repo entry is pruned, not left as []")
}

func TestStore_Remove_IsIdempotent(t *testing.T) {
	s := newTestStore(t)
	// Removing a branch that was never protected is a no-op, not an error.
	require.NoError(t, s.Remove("/repo", "main"))

	got, err := s.Load()
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestStore_Remove_RelativePathMatchesAbsoluteAdd(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.Add(".", "main"))

	// Removing with a relative form must resolve to the same absolute repo.
	require.NoError(t, s.Remove(".", "main"))

	got, err := s.Load()
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestStore_Flat_UnionAcrossReposDedupSorted(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.Add("/repo-a", "integration"))
	require.NoError(t, s.Add("/repo-a", "release"))
	require.NoError(t, s.Add("/repo-b", "integration")) // dup across repos
	require.NoError(t, s.Add("/repo-b", "hotfix"))

	got, err := s.Flat()
	require.NoError(t, err)
	assert.Equal(t, []string{"hotfix", "integration", "release"}, got, "union is dedup'd and sorted")
}

func TestStore_Flat_EmptyWhenNoProtectedBranches(t *testing.T) {
	s := newTestStore(t)
	got, err := s.Flat()
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestStore_Flat_SurvivesCorruptFile(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(s.path), 0o755))
	require.NoError(t, os.WriteFile(s.path, []byte("not json"), 0o644))

	got, err := s.Flat()
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestStore_RoundTrip_AddRemoveAdd proves the store survives a remove that
// empties a repo, then a re-add of the same repo+branch — no stale state.
func TestStore_RoundTrip_AddRemoveAdd(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.Add("/repo", "main"))
	require.NoError(t, s.Remove("/repo", "main"))
	require.NoError(t, s.Add("/repo", "main"))

	got, err := s.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"main"}, got["/repo"])

	flat, err := s.Flat()
	require.NoError(t, err)
	assert.Equal(t, []string{"main"}, flat)
}

// TestStore_Flat_IsStableAcrossReloads proves the union is stable (sorted,
// dedup'd) after multiple reloads — SIGHUP must not produce a different set.
func TestStore_Flat_IsStableAcrossReloads(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.Add("/repo-a", "b2"))
	require.NoError(t, s.Add("/repo-a", "a1"))
	require.NoError(t, s.Add("/repo-b", "a1"))

	first, err := s.Flat()
	require.NoError(t, err)
	second, err := s.Flat()
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.True(t, sort.StringsAreSorted(first), "union is sorted for stable reload")
}
