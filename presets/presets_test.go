package presets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStoreAt(filepath.Join(t.TempDir(), "presets.json"))
}

func TestStoreListEmptyWhenAbsent(t *testing.T) {
	s := newTestStore(t)

	names, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, names)
}

func TestStoreGetMissingReturnsFalse(t *testing.T) {
	s := newTestStore(t)

	_, ok, err := s.Get("Nope")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestStoreSetGetRoundTrip(t *testing.T) {
	s := newTestStore(t)
	repo := t.TempDir()

	require.NoError(t, s.Set("CS2 Work", Preset{
		Repo:    repo,
		Host:    "local",
		Profile: "Pi",
	}))

	p, ok, err := s.Get("CS2 Work")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, repo, p.Repo) // already absolute (TempDir)
	assert.Equal(t, "local", p.Host)
	assert.Equal(t, "Pi", p.Profile)
}

func TestStoreSetResolvesRelativeRepo(t *testing.T) {
	s := newTestStore(t)
	abs, err := filepath.Abs(".")
	require.NoError(t, err)

	// A relative path is stored as its absolute form.
	require.NoError(t, s.Set("rel", Preset{Repo: "."}))

	p, ok, err := s.Get("rel")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, abs, p.Repo)
}

func TestStoreSetOverwritesExisting(t *testing.T) {
	s := newTestStore(t)
	repo := t.TempDir()

	require.NoError(t, s.Set("work", Preset{Repo: repo, Profile: "Pi"}))
	require.NoError(t, s.Set("work", Preset{Repo: repo, Profile: "Claude"}))

	p, ok, err := s.Get("work")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "Claude", p.Profile)
}

func TestStoreListIsSorted(t *testing.T) {
	s := newTestStore(t)
	repo := t.TempDir()

	require.NoError(t, s.Set("Zebra", Preset{Repo: repo}))
	require.NoError(t, s.Set("Alpha", Preset{Repo: repo}))
	require.NoError(t, s.Set("Mid", Preset{Repo: repo}))

	names, err := s.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"Alpha", "Mid", "Zebra"}, names)
}

func TestStoreRemove(t *testing.T) {
	s := newTestStore(t)
	repo := t.TempDir()

	require.NoError(t, s.Set("work", Preset{Repo: repo}))
	require.NoError(t, s.Remove("work"))

	_, ok, err := s.Get("work")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestStoreRemoveUnknownIsNoop(t *testing.T) {
	s := newTestStore(t)
	// Removing from an empty store must not error nor create a file.
	require.NoError(t, s.Remove("ghost"))
	_, err := os.Stat(s.Path())
	assert.True(t, os.IsNotExist(err), "no-op remove must not create the file")
}

func TestStoreRejectsEmptyName(t *testing.T) {
	s := newTestStore(t)
	err := s.Set("", Preset{Repo: t.TempDir()})
	assert.Error(t, err)
}

func TestStoreSelfHealsCorruptFile(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.WriteFile(s.Path(), []byte("{not json"), 0644))

	// A corrupt file is treated as empty, not an error.
	names, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, names)

	// And the store is usable afterwards.
	require.NoError(t, s.Set("work", Preset{Repo: t.TempDir()}))
	p, ok, err := s.Get("work")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.NotEmpty(t, p.Repo)
}

func TestStoreReloadsBetweenCalls(t *testing.T) {
	// read-on-open: a second Store pointing at the same file sees writes made
	// by the first, with no watcher. This is the "hot-reload" contract.
	s1 := newTestStore(t)
	repo := t.TempDir()
	require.NoError(t, s1.Set("work", Preset{Repo: repo}))

	s2 := NewStoreAt(s1.Path())
	names, err := s2.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"work"}, names)
}
