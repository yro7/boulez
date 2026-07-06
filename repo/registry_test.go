package repo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRegistry creates a Registry backed by a temp file, isolated from the
// real ~/.boulez/ state.
func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	return &Registry{path: filepath.Join(t.TempDir(), "repos.json")}
}

const testLocal = "local"

func TestRegistryListEmptyWhenAbsent(t *testing.T) {
	r := newTestRegistry(t)

	entries, err := r.List()
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestRegistryAddResolvesAbsoluteAndPersists(t *testing.T) {
	r := newTestRegistry(t)

	rel := "."
	err := r.Add(rel, testLocal)
	require.NoError(t, err)

	entries, err := r.List()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	abs, err := filepath.Abs(rel)
	require.NoError(t, err)
	assert.Equal(t, abs, entries[0].Path)
	assert.Equal(t, testLocal, entries[0].Host)
	assert.True(t, filepath.IsAbs(entries[0].Path))
}

func TestRegistryAddDedupes(t *testing.T) {
	r := newTestRegistry(t)
	abs, err := filepath.Abs(".")
	require.NoError(t, err)

	require.NoError(t, r.Add(abs, testLocal))
	require.NoError(t, r.Add(abs, testLocal)) // exact duplicate
	require.NoError(t, r.Add(".", testLocal)) // resolves to same absolute path

	entries, err := r.List()
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.True(t, r.Contains(abs, testLocal))
}

func TestRegistryAddSamePathDifferentHostsDoesNotDedup(t *testing.T) {
	r := newTestRegistry(t)
	const remote = "dev-machine"

	require.NoError(t, r.Add("/root/proj", testLocal))
	require.NoError(t, r.Add("/root/proj", remote))

	entries, err := r.List()
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.True(t, r.Contains("/root/proj", testLocal))
	assert.True(t, r.Contains("/root/proj", remote))
}

func TestRegistryAddPreservesInsertionOrder(t *testing.T) {
	r := newTestRegistry(t)

	first := t.TempDir()
	second := t.TempDir()
	require.NoError(t, r.Add(first, testLocal))
	require.NoError(t, r.Add(second, testLocal))

	entries, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, first, entries[0].Path)
	assert.Equal(t, second, entries[1].Path)
}

func TestRegistryRemoveIsIdempotent(t *testing.T) {
	r := newTestRegistry(t)
	dir := t.TempDir()

	require.NoError(t, r.Add(dir, testLocal))
	require.True(t, r.Contains(dir, testLocal))

	require.NoError(t, r.Remove(dir, testLocal))
	entries, err := r.List()
	require.NoError(t, err)
	assert.Empty(t, entries)

	// Removing again is a no-op, not an error.
	require.NoError(t, r.Remove(dir, testLocal))
}

func TestRegistryRemoveIsHostScoped(t *testing.T) {
	r := newTestRegistry(t)
	const remote = "dev-machine"
	const path = "/root/proj"

	require.NoError(t, r.Add(path, testLocal))
	require.NoError(t, r.Add(path, remote))

	// Removing the local binding leaves the remote one.
	require.NoError(t, r.Remove(path, testLocal))
	entries, err := r.List()
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, remote, entries[0].Host)
	assert.True(t, r.Contains(path, remote))
	assert.False(t, r.Contains(path, testLocal))
}

func TestRegistryRemovePreservesOrder(t *testing.T) {
	r := newTestRegistry(t)
	a := t.TempDir()
	b := t.TempDir()
	c := t.TempDir()

	require.NoError(t, r.Add(a, testLocal))
	require.NoError(t, r.Add(b, testLocal))
	require.NoError(t, r.Add(c, testLocal))

	require.NoError(t, r.Remove(b, testLocal))
	entries, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{a, c}, entryPaths(entries))
}

func TestRegistryTouchMovesPathToHead(t *testing.T) {
	r := newTestRegistry(t)
	a := t.TempDir()
	b := t.TempDir()
	c := t.TempDir()

	require.NoError(t, r.Add(a, testLocal))
	require.NoError(t, r.Add(b, testLocal))
	require.NoError(t, r.Add(c, testLocal))

	// Touch b → b moves to head, a and c keep relative order.
	require.NoError(t, r.Touch(b, testLocal))
	entries, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{b, a, c}, entryPaths(entries))
}

func TestRegistryTouchIsHostScoped(t *testing.T) {
	r := newTestRegistry(t)
	const remote = "dev-machine"
	const path = "/root/proj"

	require.NoError(t, r.Add("/a", testLocal))
	require.NoError(t, r.Add(path, testLocal))
	require.NoError(t, r.Add(path, remote))

	// Touching the local binding must not move/merge the remote one.
	require.NoError(t, r.Touch(path, testLocal))
	entries, err := r.List()
	require.NoError(t, err)
	// Head is the touched local entry; the remote entry stays a distinct row.
	require.Len(t, entries, 3)
	assert.Equal(t, path, entries[0].Path)
	assert.Equal(t, testLocal, entries[0].Host)
	assert.Equal(t, remote, entries[2].Host)
}

func TestRegistryTouchIsIdempotent(t *testing.T) {
	r := newTestRegistry(t)
	a := t.TempDir()
	b := t.TempDir()
	require.NoError(t, r.Add(a, testLocal))
	require.NoError(t, r.Add(b, testLocal))

	require.NoError(t, r.Touch(a, testLocal))
	require.NoError(t, r.Touch(a, testLocal)) // touching the head again is a no-op persist-wise
	entries, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{a, b}, entryPaths(entries))
}

func TestRegistryTouchUnknownIsNoOp(t *testing.T) {
	r := newTestRegistry(t)
	a := t.TempDir()
	require.NoError(t, r.Add(a, testLocal))

	require.NoError(t, r.Touch(t.TempDir(), testLocal)) // not registered
	entries, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{a}, entryPaths(entries))
}

func TestRegistryTouchResolvesRelative(t *testing.T) {
	r := newTestRegistry(t)
	abs, err := filepath.Abs(".")
	require.NoError(t, err)
	require.NoError(t, r.Add(abs, testLocal))

	require.NoError(t, r.Touch(".", testLocal)) // relative resolves to the registered abs path
	entries, err := r.List()
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, abs, entries[0].Path)
}

func TestRegistryContainsHandlesRelativePath(t *testing.T) {
	r := newTestRegistry(t)
	abs, err := filepath.Abs(".")
	require.NoError(t, err)
	require.NoError(t, r.Add(abs, testLocal))

	assert.True(t, r.Contains(abs, testLocal))
	assert.True(t, r.Contains(".", testLocal)) // resolved to absolute
	assert.False(t, r.Contains("/nonexistent/path", testLocal))
}

func TestRegistryContainsIsHostScoped(t *testing.T) {
	r := newTestRegistry(t)
	const remote = "dev-machine"
	const path = "/root/proj"

	require.NoError(t, r.Add(path, testLocal))
	assert.False(t, r.Contains(path, remote), "path added to local must not be present for a remote host")
}

func TestRegistryListByHostFiltersAndPreservesOrder(t *testing.T) {
	r := newTestRegistry(t)
	const remote = "dev-machine"

	require.NoError(t, r.Add("/local-a", testLocal))
	require.NoError(t, r.Add("/remote-a", remote))
	require.NoError(t, r.Add("/local-b", testLocal))
	require.NoError(t, r.Add("/remote-b", remote))

	local, err := r.ListByHost(testLocal)
	require.NoError(t, err)
	assert.Equal(t, []string{"/local-a", "/local-b"}, local)

	remotes, err := r.ListByHost(remote)
	require.NoError(t, err)
	assert.Equal(t, []string{"/remote-a", "/remote-b"}, remotes)

	// Empty host is treated as local.
	empty, err := r.ListByHost("")
	require.NoError(t, err)
	assert.Equal(t, []string{"/local-a", "/local-b"}, empty)
}

func TestRegistryPersistenceRoundTrip(t *testing.T) {
	r := newTestRegistry(t)
	a := t.TempDir()
	b := t.TempDir()
	require.NoError(t, r.Add(a, testLocal))
	require.NoError(t, r.Add(b, testLocal))

	// A fresh Registry pointing at the same file must load the saved state.
	r2 := &Registry{path: r.path}
	loaded, err := r2.List()
	require.NoError(t, err)
	assert.Equal(t, []string{a, b}, entryPaths(loaded))
	assert.True(t, r2.Contains(a, testLocal))
}

func TestRegistryCorruptFileYieldsEmptyList(t *testing.T) {
	r := newTestRegistry(t)
	require.NoError(t, os.WriteFile(r.path, []byte("{not json"), 0644))

	entries, err := r.List()
	require.NoError(t, err) // corrupt file is treated as empty, not a fatal error
	assert.Empty(t, entries)
}

func TestNewRegistryUsesConfigDir(t *testing.T) {
	originalHome := os.Getenv("HOME")
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	defer func() { os.Setenv("HOME", originalHome) }()

	r, err := NewRegistry()
	require.NoError(t, err)

	// The backing file lives under the boulez config dir.
	assert.True(t, filepath.IsAbs(r.path))
	assert.True(t, filepath.HasPrefix(r.path, filepath.Join(tempHome, ".boulez")))
	assert.Equal(t, "repos.json", filepath.Base(r.path))
}

// entryPaths projects a slice of entries to just their paths, for concise
// order assertions.
func entryPaths(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Path)
	}
	return out
}
