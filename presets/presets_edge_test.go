package presets

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStore_FullFieldRoundTrip proves every Preset field survives a Set→Get
// round-trip, not just Repo/Host/Profile. Prompt and Branch are the fields
// most likely to be dropped by a struct-tag or json-format regression.
func TestStore_FullFieldRoundTrip(t *testing.T) {
	s := newTestStore(t)
	repo := t.TempDir()

	in := Preset{
		Repo:    repo,
		Host:    "dev-machine",
		Profile: "Pi",
		Prompt:  "Fix the bug in module X",
		Branch:  "feature/fix-x",
	}
	require.NoError(t, s.Set("work", in))

	got, ok, err := s.Get("work")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, in.Repo, got.Repo)
	assert.Equal(t, in.Host, got.Host)
	assert.Equal(t, in.Profile, got.Profile)
	assert.Equal(t, in.Prompt, got.Prompt, "Prompt must survive the round-trip")
	assert.Equal(t, in.Branch, got.Branch, "Branch must survive the round-trip")
}

// TestStore_EmptyFieldsRoundTrip proves a preset with only the required Repo
// field set (empty Host/Profile/Prompt/Branch) round-trips without error and
// the omitted fields come back as empty strings (not "null" / not missing).
func TestStore_EmptyFieldsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	repo := t.TempDir()

	require.NoError(t, s.Set("minimal", Preset{Repo: repo}))

	got, ok, err := s.Get("minimal")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, repo, got.Repo)
	assert.Empty(t, got.Host)
	assert.Empty(t, got.Profile)
	assert.Empty(t, got.Prompt)
	assert.Empty(t, got.Branch)
}

// TestStore_GetOnCorruptFile proves a corrupt file does not error on Get; it
// is treated as "no preset found" (self-heal). A bad presets.json must never
// block the Ctrl+R flow.
func TestStore_GetOnCorruptFile(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.WriteFile(s.Path(), []byte("{not json"), 0644))

	_, ok, err := s.Get("anything")
	require.NoError(t, err, "corrupt file must self-heal, not error")
	assert.False(t, ok, "no preset is found in a corrupt/empty store")
}

// TestStore_ListOnCorruptFileIsSortedEmpty proves List on a corrupt file
// returns an empty sorted slice, not an error. The selector relies on List
// never erroring.
func TestStore_ListOnCorruptFileIsSortedEmpty(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.WriteFile(s.Path(), []byte("garbage"), 0644))

	names, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, names)
}

// TestStore_StringReportsPathAndCount proves the debug String() includes the
// path and the entry count. Used by `boulez ctl` debug output.
func TestStore_StringReportsPathAndCount(t *testing.T) {
	s := newTestStore(t)
	// Empty store.
	str := s.String()
	assert.Contains(t, str, s.Path())
	assert.Contains(t, str, "0 presets")

	// With entries.
	require.NoError(t, s.Set("a", Preset{Repo: t.TempDir()}))
	require.NoError(t, s.Set("b", Preset{Repo: t.TempDir()}))
	str = s.String()
	assert.Contains(t, str, "2 presets")
}

// TestStore_StringOnCorruptFile proves String degrades gracefully on a
// corrupt file (reports unreadable, not a panic).
func TestStore_StringOnCorruptFile(t *testing.T) {
	s := newTestStore(t)
	// Write a file that exists but is unreadable as JSON.
	require.NoError(t, os.WriteFile(s.Path(), []byte("not json"), 0644))
	// Self-heal means load() returns empty map, so String reports 0 presets.
	str := s.String()
	assert.Contains(t, str, "0 presets")
}

// TestStore_SetWithEmptyRepo proves Set with an empty Repo path resolves it
// to the absolute cwd (filepath.Abs("") == cwd). This is the documented
// behaviour; pinning it so a future "reject empty repo" change fails loudly.
func TestStore_SetWithEmptyRepo(t *testing.T) {
	s := newTestStore(t)
	cwd, err := filepath.Abs(".")
	require.NoError(t, err)

	require.NoError(t, s.Set("empty-repo", Preset{Repo: ""}))

	p, ok, err := s.Get("empty-repo")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, cwd, p.Repo, "empty repo resolves to the absolute cwd")
}

// TestStore_RemoveOnCorruptFile proves Remove on a corrupt file is a no-op
// (the corrupt state is treated as empty, so nothing matches).
func TestStore_RemoveOnCorruptFile(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.WriteFile(s.Path(), []byte("not json"), 0644))

	require.NoError(t, s.Remove("ghost"), "remove on a corrupt file must not error")
}

// TestStore_UnknownJSONFieldsIgnored proves forward-compat: a presets file
// with extra/unknown fields (from a future version) loads without error and
// the known fields are preserved. Guards against a json strictness regression.
func TestStore_UnknownJSONFieldsIgnored(t *testing.T) {
	s := newTestStore(t)
	repo := t.TempDir()
	// Hand-craft a file with a known preset plus an unknown future field.
	raw := map[string]any{
		"work": map[string]any{
			"repo":        repo,
			"host":        "local",
			"profile":     "Pi",
			"futureField": "some-day-extension",
		},
	}
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(s.Path(), data, 0644))

	p, ok, err := s.Get("work")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, repo, p.Repo)
	assert.Equal(t, "local", p.Host)
	assert.Equal(t, "Pi", p.Profile)
}

// TestStore_SetPersistsPromptAndBranchTogether proves a Set that overwrites an
// existing preset with a different Prompt/Branch fully replaces them (no
// stale leftover from the previous version).
func TestStore_SetOverwritesAllFields(t *testing.T) {
	s := newTestStore(t)
	repo := t.TempDir()

	require.NoError(t, s.Set("work", Preset{
		Repo: repo, Profile: "Pi", Prompt: "first", Branch: "b1",
	}))
	require.NoError(t, s.Set("work", Preset{
		Repo: repo, Profile: "Claude", Prompt: "second", Branch: "b2",
	}))

	p, ok, err := s.Get("work")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "Claude", p.Profile)
	assert.Equal(t, "second", p.Prompt, "Prompt must be fully replaced, not merged")
	assert.Equal(t, "b2", p.Branch, "Branch must be fully replaced, not merged")
}

// TestStore_ListAlphabeticalAfterReload proves the alphabetical ordering is
// stable across a reload (a second Store over the same file), so the selector
// does not reorder between Ctrl+R opens.
func TestStore_ListAlphabeticalAfterReload(t *testing.T) {
	s1 := newTestStore(t)
	repo := t.TempDir()
	for _, n := range []string{"Zulu", "Alpha", "Mike"} {
		require.NoError(t, s1.Set(n, Preset{Repo: repo}))
	}

	s2 := NewStoreAt(s1.Path())
	names, err := s2.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"Alpha", "Mike", "Zulu"}, names, "order stable across reload")
}
