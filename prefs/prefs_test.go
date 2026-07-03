package prefs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStoreAt(filepath.Join(t.TempDir(), "preferences.json"))
}

func TestStoreGetEmptyWhenAbsent(t *testing.T) {
	s := newTestStore(t)

	p, ok, err := s.Get("/some/repo")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, Preference{}, p)
}

func TestStoreSetGetRoundTrip(t *testing.T) {
	s := newTestStore(t)
	repo := t.TempDir()

	require.NoError(t, s.Set(repo, "Pi", "pi"))

	p, ok, err := s.Get(repo)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, Preference{Profile: "Pi", Program: "pi"}, p)
}

func TestStoreGetResolvesRelativePath(t *testing.T) {
	s := newTestStore(t)
	abs, err := filepath.Abs(".")
	require.NoError(t, err)
	require.NoError(t, s.Set(abs, "Pi", "pi"))

	// A relative path resolving to the same absolute path finds the pref.
	p, ok, err := s.Get(".")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "Pi", p.Profile)
}

func TestStoreSetOverwritesExisting(t *testing.T) {
	s := newTestStore(t)
	repo := t.TempDir()

	require.NoError(t, s.Set(repo, "Pi", "pi"))
	require.NoError(t, s.Set(repo, "Claude", "claude"))

	p, ok, err := s.Get(repo)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, Preference{Profile: "Claude", Program: "claude"}, p)
}

func TestStoreClearIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	repo := t.TempDir()
	require.NoError(t, s.Set(repo, "Pi", "pi"))

	require.NoError(t, s.Clear(repo))
	p, ok, err := s.Get(repo)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, Preference{}, p)

	// Clearing again is a no-op.
	require.NoError(t, s.Clear(repo))
}

func TestStorePersistenceRoundTrip(t *testing.T) {
	s := newTestStore(t)
	repo := t.TempDir()
	require.NoError(t, s.Set(repo, "Pi", "pi"))

	// A fresh Store over the same file loads the saved preferences.
	s2 := NewStoreAt(s.Path())
	p, ok, err := s2.Get(repo)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, Preference{Profile: "Pi", Program: "pi"}, p)
}

func TestStoreCorruptFileYieldsEmpty(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.WriteFile(s.Path(), []byte("{not json"), 0644))

	p, ok, err := s.Get("/anything")
	require.NoError(t, err, "corrupt file should self-heal to empty")
	assert.False(t, ok)
	assert.Equal(t, Preference{}, p)
}

func TestNewStoreUsesConfigDir(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	s, err := NewStore()
	require.NoError(t, err)

	assert.True(t, filepath.IsAbs(s.Path()))
	assert.True(t, filepath.HasPrefix(s.Path(), filepath.Join(tempHome, ".cs2")))
	assert.Equal(t, "preferences.json", filepath.Base(s.Path()))
}
