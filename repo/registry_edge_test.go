package repo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegistry_RemoveOnMissingFileDoesNotCreateFile proves a no-op Remove
// against a cold-start (missing file) registry does NOT create an empty file.
// This matters because a stray empty repos.json would be a confusing artefact
// for a user who never added a repo.
func TestRegistry_RemoveOnMissingFileDoesNotCreateFile(t *testing.T) {
	r := newTestRegistry(t)
	require.NoError(t, r.Remove("/nonexistent", testLocal))
	_, err := os.Stat(r.path)
	assert.True(t, os.IsNotExist(err), "no-op remove must not create the file")
}

// TestRegistry_TouchOnMissingFileDoesNotCreateFile proves a Touch of an
// unknown path on a cold-start registry does not create a file either.
func TestRegistry_TouchOnMissingFileDoesNotCreateFile(t *testing.T) {
	r := newTestRegistry(t)
	require.NoError(t, r.Touch(t.TempDir(), testLocal))
	_, err := os.Stat(r.path)
	assert.True(t, os.IsNotExist(err), "no-op touch must not create the file")
}

// TestRegistry_AddAfterCorruptFile proves the registry is writable after a
// corrupt file was present (self-heal does not leave it read-only). A bad
// repos.json must not permanently wedge the Add path.
func TestRegistry_AddAfterCorruptFile(t *testing.T) {
	r := newTestRegistry(t)
	require.NoError(t, os.WriteFile(r.path, []byte("{not json"), 0644))

	dir := t.TempDir()
	require.NoError(t, r.Add(dir, testLocal), "Add must overwrite the corrupt file with valid JSON")

	entries, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{dir}, entryPaths(entries))
}

// TestRegistry_ContainsOnCorruptFileReturnsFalse proves Contains degrades to
// "not present" (not an error/panic) when the file is corrupt. The selector
// relies on Contains never erroring.
func TestRegistry_ContainsOnCorruptFileReturnsFalse(t *testing.T) {
	r := newTestRegistry(t)
	require.NoError(t, os.WriteFile(r.path, []byte("garbage"), 0644))
	assert.False(t, r.Contains("/anything", testLocal))
}

// TestRegistry_TouchUnknownDoesNotCreateHeadEntry proves touching an unknown path
// on a populated registry does not create a spurious head entry.
func TestRegistry_TouchUnknownDoesNotCreateHeadEntry(t *testing.T) {
	r := newTestRegistry(t)
	a := t.TempDir()
	require.NoError(t, r.Add(a, testLocal))

	require.NoError(t, r.Touch(t.TempDir(), testLocal)) // unknown
	entries, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{a}, entryPaths(entries), "unknown touch must not add an entry")
}

// TestRegistry_RemoveAllThenListEmpty proves removing the last entry leaves an
// empty list (the file may exist with an empty array, which is fine).
func TestRegistry_RemoveAllThenListEmpty(t *testing.T) {
	r := newTestRegistry(t)
	a := t.TempDir()
	require.NoError(t, r.Add(a, testLocal))
	require.NoError(t, r.Remove(a, testLocal))

	entries, err := r.List()
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// TestRegistry_NewRegistryAt_Path proves the path accessor returns the
// injected path (used by tests and debug output).
func TestRegistry_NewRegistryAt_Path(t *testing.T) {
	r := NewRegistryAt("/some/path/repos.json")
	assert.Equal(t, "/some/path/repos.json", r.Path())
}

// TestRegistry_RemoveRelativeMatchesAbsoluteAdd proves Remove with a relative
// path resolves to the same absolute path as Add, so a relative remove of an
// absolutely-added repo actually removes it (the absolute-resolution contract
// must be symmetric across Add/Remove/Touch/Contains). Applies to the local
// host; remote hosts compare verbatim.
func TestRegistry_RemoveRelativeMatchesAbsoluteAdd(t *testing.T) {
	r := newTestRegistry(t)
	abs, err := filepath.Abs(".")
	require.NoError(t, err)
	require.NoError(t, r.Add(abs, testLocal))

	// Remove via the relative form; must resolve to the same absolute path.
	require.NoError(t, r.Remove(".", testLocal))

	entries, err := r.List()
	require.NoError(t, err)
	assert.Empty(t, entries, "relative remove must match absolute add")
	assert.False(t, r.Contains(abs, testLocal))
}
