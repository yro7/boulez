package repo

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFlat seeds a registry file with the legacy flat `[]string` shape.
func writeFlat(t *testing.T, path string, paths []string) {
	t.Helper()
	data, err := json.MarshalIndent(paths, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0644))
}

// TestRegistry_MigratesFlatListToHostAware proves a legacy flat `["/p1","/p2"]`
// repos.json is migrated in place to the v1 {path,host} shape on first load:
// every bare path becomes a "local" entry, a .pre-migration backup of the
// original bytes is written, and a subsequent load reads the new shape directly
// (idempotent — no second migration, no backup rewrite).
func TestRegistry_MigratesFlatListToHostAware(t *testing.T) {
	r := newTestRegistry(t)
	orig := []string{"/Users/u/projets/a", "/root/proj/b"}
	writeFlat(t, r.path, orig)
	originalBytes, err := os.ReadFile(r.path)
	require.NoError(t, err)

	entries, err := r.List()
	require.NoError(t, err)
	require.Len(t, entries, 2)
	for _, e := range entries {
		assert.Equal(t, testLocal, e.Host, "bare paths migrate to local")
	}
	assert.Equal(t, orig[0], entries[0].Path)
	assert.Equal(t, orig[1], entries[1].Path)

	// A one-time backup of the original bytes exists.
	backup, err := os.ReadFile(r.path + ".pre-migration")
	require.NoError(t, err)
	assert.Equal(t, originalBytes, backup)

	// The file on disk is now the v1 shape (version=1).
	var v1 registryV1
	require.NoError(t, json.Unmarshal(mustReadFile(t, r.path), &v1))
	assert.Equal(t, registryVersion, v1.Version)
	assert.Len(t, v1.Entries, 2)

	// Reloading is idempotent: entries unchanged, backup NOT rewritten.
	// Tweak the backup's mtime sentinel by overwriting it, then reload and
	// confirm the backup is preserved as-is (migration must not run again).
	require.NoError(t, os.WriteFile(r.path+".pre-migration", []byte("sentinel"), 0644))
	entries2, err := r.List()
	require.NoError(t, err)
	require.Len(t, entries2, 2)
	backup2, err := os.ReadFile(r.path + ".pre-migration")
	require.NoError(t, err)
	assert.Equal(t, "sentinel", string(backup2), "re-load must not overwrite the existing backup")
}

// TestRegistry_MigrationPreservesOrder proves the migration keeps the original
// insertion order of the flat list (MRU/insertion contract is preserved).
func TestRegistry_MigrationPreservesOrder(t *testing.T) {
	r := newTestRegistry(t)
	writeFlat(t, r.path, []string{"/first", "/second", "/third"})

	entries, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"/first", "/second", "/third"}, entryPaths(entries))
}

// TestRegistry_LoadNewFormatRoundTrip proves a hand-written v1 file loads
// as-is (no migration, no backup) and preserves host bindings.
func TestRegistry_LoadNewFormatRoundTrip(t *testing.T) {
	r := newTestRegistry(t)
	seed := registryV1{Version: registryVersion, Entries: []Entry{
		{Path: "/local-a", Host: testLocal},
		{Path: "/root/proj", Host: "dev-machine"},
	}}
	data, err := json.MarshalIndent(seed, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(r.path, data, 0644))

	entries, err := r.List()
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "dev-machine", entries[1].Host)

	// No backup is written for an already-v1 file.
	_, statErr := os.Stat(r.path + ".pre-migration")
	assert.True(t, os.IsNotExist(statErr), "v1 file must not trigger a backup")
}

// TestRegistry_LoadFillsEmptyHostWithLocal proves a hand-edited v1 file that
// omits a Host value is normalized to "local" (defensive against partial edits).
func TestRegistry_LoadFillsEmptyHostWithLocal(t *testing.T) {
	r := newTestRegistry(t)
	// Hand-write entries with an empty Host.
	require.NoError(t, os.WriteFile(r.path, []byte(`{"version":1,"entries":[{"path":"/p","host":""}]}`), 0644))

	entries, err := r.List()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, testLocal, entries[0].Host)
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}
