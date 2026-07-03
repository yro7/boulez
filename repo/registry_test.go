package repo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRegistry creates a Registry backed by a temp file, isolated from the
// real ~/.cs2/ state.
func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	return &Registry{path: filepath.Join(t.TempDir(), "repos.json")}
}

func TestRegistryListEmptyWhenAbsent(t *testing.T) {
	r := newTestRegistry(t)

	paths, err := r.List()
	require.NoError(t, err)
	assert.Empty(t, paths)
}

func TestRegistryAddResolvesAbsoluteAndPersists(t *testing.T) {
	r := newTestRegistry(t)

	rel := "."
	err := r.Add(rel)
	require.NoError(t, err)

	paths, err := r.List()
	require.NoError(t, err)
	require.Len(t, paths, 1)
	abs, err := filepath.Abs(rel)
	require.NoError(t, err)
	assert.Equal(t, abs, paths[0])
	assert.True(t, filepath.IsAbs(paths[0]))
}

func TestRegistryAddDedupes(t *testing.T) {
	r := newTestRegistry(t)
	abs, err := filepath.Abs(".")
	require.NoError(t, err)

	require.NoError(t, r.Add(abs))
	require.NoError(t, r.Add(abs)) // exact duplicate
	require.NoError(t, r.Add(".")) // resolves to same absolute path

	paths, err := r.List()
	require.NoError(t, err)
	assert.Len(t, paths, 1)
	assert.True(t, r.Contains(abs))
}

func TestRegistryAddPreservesInsertionOrder(t *testing.T) {
	r := newTestRegistry(t)

	first := t.TempDir()
	second := t.TempDir()
	require.NoError(t, r.Add(first))
	require.NoError(t, r.Add(second))

	paths, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{first, second}, paths)
}

func TestRegistryRemoveIsIdempotent(t *testing.T) {
	r := newTestRegistry(t)
	dir := t.TempDir()

	require.NoError(t, r.Add(dir))
	require.True(t, r.Contains(dir))

	require.NoError(t, r.Remove(dir))
	paths, err := r.List()
	require.NoError(t, err)
	assert.Empty(t, paths)

	// Removing again is a no-op, not an error.
	require.NoError(t, r.Remove(dir))
}

func TestRegistryRemovePreservesOrder(t *testing.T) {
	r := newTestRegistry(t)
	a := t.TempDir()
	b := t.TempDir()
	c := t.TempDir()

	require.NoError(t, r.Add(a))
	require.NoError(t, r.Add(b))
	require.NoError(t, r.Add(c))

	require.NoError(t, r.Remove(b))
	paths, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{a, c}, paths)
}

func TestRegistryTouchMovesPathToHead(t *testing.T) {
	r := newTestRegistry(t)
	a := t.TempDir()
	b := t.TempDir()
	c := t.TempDir()

	require.NoError(t, r.Add(a))
	require.NoError(t, r.Add(b))
	require.NoError(t, r.Add(c))

	// Touch b → b moves to head, a and c keep relative order.
	require.NoError(t, r.Touch(b))
	paths, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{b, a, c}, paths)
}

func TestRegistryTouchIsIdempotent(t *testing.T) {
	r := newTestRegistry(t)
	a := t.TempDir()
	b := t.TempDir()
	require.NoError(t, r.Add(a))
	require.NoError(t, r.Add(b))

	require.NoError(t, r.Touch(a))
	require.NoError(t, r.Touch(a)) // touching the head again is a no-op persist-wise
	paths, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{a, b}, paths)
}

func TestRegistryTouchUnknownIsNoOp(t *testing.T) {
	r := newTestRegistry(t)
	a := t.TempDir()
	require.NoError(t, r.Add(a))

	require.NoError(t, r.Touch(t.TempDir())) // not registered
	paths, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{a}, paths)
}

func TestRegistryTouchResolvesRelative(t *testing.T) {
	r := newTestRegistry(t)
	abs, err := filepath.Abs(".")
	require.NoError(t, err)
	require.NoError(t, r.Add(abs))

	require.NoError(t, r.Touch(".")) // relative resolves to the registered abs path
	paths, err := r.List()
	require.NoError(t, err)
	assert.Len(t, paths, 1)
	assert.Equal(t, abs, paths[0])
}

func TestRegistryContainsHandlesRelativePath(t *testing.T) {
	r := newTestRegistry(t)
	abs, err := filepath.Abs(".")
	require.NoError(t, err)
	require.NoError(t, r.Add(abs))

	assert.True(t, r.Contains(abs))
	assert.True(t, r.Contains(".")) // resolved to absolute
	assert.False(t, r.Contains("/nonexistent/path"))
}

func TestRegistryPersistenceRoundTrip(t *testing.T) {
	r := newTestRegistry(t)
	a := t.TempDir()
	b := t.TempDir()
	require.NoError(t, r.Add(a))
	require.NoError(t, r.Add(b))

	// A fresh Registry pointing at the same file must load the saved state.
	r2 := &Registry{path: r.path}
	loaded, err := r2.List()
	require.NoError(t, err)
	assert.Equal(t, []string{a, b}, loaded)
	assert.True(t, r2.Contains(a))
}

func TestRegistryCorruptFileYieldsEmptyList(t *testing.T) {
	r := newTestRegistry(t)
	require.NoError(t, os.WriteFile(r.path, []byte("{not json"), 0644))

	paths, err := r.List()
	require.NoError(t, err) // corrupt file is treated as empty, not a fatal error
	assert.Empty(t, paths)
}

func TestNewRegistryUsesConfigDir(t *testing.T) {
	originalHome := os.Getenv("HOME")
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	defer func() { os.Setenv("HOME", originalHome) }()

	r, err := NewRegistry()
	require.NoError(t, err)

	// The backing file lives under the cs2 config dir.
	assert.True(t, filepath.IsAbs(r.path))
	assert.True(t, filepath.HasPrefix(r.path, filepath.Join(tempHome, ".cs2")))
	assert.Equal(t, "repos.json", filepath.Base(r.path))
}
