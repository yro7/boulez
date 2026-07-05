package protected

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStore_Load_DeduplicatesBranchesWithinRepo proves Load cleans a
// hand-authored JSON file that lists the same branch twice for a repo. The
// daemon must not treat a duplicate as two distinct protections, and the
// kernel's Flat() union must be dedup'd across reloads (a SIGHUP must not
// duplicate entries).
func TestStore_Load_DeduplicatesBranchesWithinRepo(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(s.path), 0o755))
	// Hand-craft a file with a duplicate branch for /repo.
	raw := map[string][]string{
		"/repo": {"main", "integration", "main", "release"},
	}
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(s.path, data, 0o644))

	got, err := s.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"main", "integration", "release"}, got["/repo"],
		"duplicate branches must be collapsed on Load")
}

// TestStore_Load_FiltersEmptyBranchNames proves an empty-string branch name
// in a hand-authored (or legacy) file is dropped on Load, not persisted into
// the in-memory map. Empty branch names are meaningless and would confuse the
// kernel's flat guard.
func TestStore_Load_FiltersEmptyBranchNames(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(s.path), 0o755))
	raw := map[string][]string{
		"/repo": {"main", "", "release", ""},
	}
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(s.path, data, 0o644))

	got, err := s.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"main", "release"}, got["/repo"],
		"empty branch names must be filtered on Load")
}

// TestStore_Load_EmptyFileYieldsEmptyMap proves a zero-length file (not just
// a missing one) is a valid cold start and yields an empty, non-nil map.
// `truncate -s0 protected.json` must not wedge the daemon.
func TestStore_Load_EmptyFileYieldsEmptyMap(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(s.path), 0o755))
	require.NoError(t, os.WriteFile(s.path, []byte{}, 0o644))

	got, err := s.Load()
	require.NoError(t, err)
	assert.NotNil(t, got, "empty file must yield a non-nil map")
	assert.Empty(t, got)
}

// TestStore_Load_EmptyBranchListForRepo proves a repo mapped to an empty
// branch slice round-trips without error (no nil-pointer, no panic).
func TestStore_Load_EmptyBranchListForRepo(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(s.path), 0o755))
	raw := map[string][]string{
		"/repo": {},
	}
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(s.path, data, 0o644))

	got, err := s.Load()
	require.NoError(t, err)
	assert.Empty(t, got["/repo"], "empty branch list is preserved as empty")
}

// TestStore_Flat_DeduplicatesAcrossReposWithLegacyDup proves the kernel-facing
// union is dedup'd even when the on-disk file lists the same branch under two
// repos (which Load already cleans within a repo, but the cross-repo dedup in
// Flat is the authoritative guard for the kernel-wide refusal).
func TestStore_Flat_DeduplicatesAcrossReposWithLegacyDup(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(s.path), 0o755))
	// Legacy file: "main" listed under two repos (and duplicated within one).
	raw := map[string][]string{
		"/repo-a": {"main", "main"},
		"/repo-b": {"main"},
	}
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(s.path, data, 0o644))

	got, err := s.Flat()
	require.NoError(t, err)
	assert.Equal(t, []string{"main"}, got, "cross-repo dup must collapse to one entry")
}

// TestStore_Add_AfterCorruptFile proves the store is usable (write succeeds)
// after a corrupt file was present at boot. The self-heal on Load must not
// leave the store in a read-only state.
func TestStore_Add_AfterCorruptFile(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(s.path), 0o755))
	require.NoError(t, os.WriteFile(s.path, []byte("not json"), 0o644))

	// Write through the corrupt state: Add must overwrite with valid JSON.
	require.NoError(t, s.Add("/repo", "main"))

	got, err := s.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"main"}, got["/repo"], "store must be writable after a corrupt file")
}

// TestStore_Remove_RelativePathDoesNotMatchDifferentRepo proves the
// absolute-resolution contract from the other direction: a relative path that
// resolves to repo X must NOT affect a different repo Y stored as absolute.
// Guards against an accidental substring match.
func TestStore_Remove_RelativePathDoesNotMatchDifferentRepo(t *testing.T) {
	s := newTestStore(t)
	// Store an absolute repo path that is NOT the cwd.
	require.NoError(t, s.Add("/totally/different/repo", "main"))

	// Removing with "." (cwd) must be a no-op against the absolute repo.
	require.NoError(t, s.Remove(".", "main"))

	got, err := s.Load()
	require.NoError(t, err)
	assert.Contains(t, got, "/totally/different/repo", "unrelated repo must be untouched")
	assert.Equal(t, []string{"main"}, got["/totally/different/repo"])
}

// TestStore_Path returns the injected path so tests/CLI can locate the file.
func TestStore_Path(t *testing.T) {
	s := NewAt("/some/path/protected.json")
	assert.Equal(t, "/some/path/protected.json", s.Path())
}

// TestNew_UsesConfigDir proves the default constructor points at the boulez
// config dir (the canonical location the daemon boots from).
func TestNew_UsesConfigDir(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	s, err := New()
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(s.Path()))
	assert.Equal(t, "protected.json", filepath.Base(s.Path()))
	assert.Contains(t, s.Path(), ".boulez")
}
