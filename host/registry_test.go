package host

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegistry_RoundTrip covers add (dedup), list (stable order), remove
// (idempotent), and persistence across instances — the deep-module contract.
func TestRegistry_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	r := NewRegistryAt(path)

	// Cold start: empty.
	got, err := r.List()
	require.NoError(t, err)
	assert.Empty(t, got)

	// Add three aliases; order preserved.
	require.NoError(t, r.Add("dev-machine"))
	require.NoError(t, r.Add("gpu-box"))
	require.NoError(t, r.Add("prod"))
	got, err = r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"dev-machine", "gpu-box", "prod"}, got)

	// Dedup: adding an existing alias is a no-op.
	require.NoError(t, r.Add("dev-machine"))
	got, err = r.List()
	require.NoError(t, err)
	assert.Len(t, got, 3, "duplicate Add must not append")

	// Contains.
	assert.True(t, r.Contains("gpu-box"))
	assert.False(t, r.Contains("nope"))

	// LocalAlias is never stored.
	require.NoError(t, r.Add(LocalAlias))
	got, err = r.List()
	require.NoError(t, err)
	assert.NotContains(t, got, LocalAlias, "local alias must stay implicit")

	// Remove preserves order of the rest.
	require.NoError(t, r.Remove("gpu-box"))
	got, err = r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"dev-machine", "prod"}, got)

	// Remove unknown is a no-op.
	require.NoError(t, r.Remove("ghost"))
	got, err = r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"dev-machine", "prod"}, got)

	// Persistence: a new Registry over the same file sees the saved aliases.
	r2 := NewRegistryAt(path)
	got, err = r2.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"dev-machine", "prod"}, got)
}

// TestRegistry_CorruptFileSelfHeals: a corrupt JSON file is treated as empty
// rather than erroring, so a bad file never blocks startup.
func TestRegistry_CorruptFileSelfHeals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0644))

	r := NewRegistryAt(path)
	got, err := r.List()
	require.NoError(t, err, "corrupt file should self-heal to empty")
	assert.Empty(t, got)
}

// TestRegistry_RemoveIsIdempotent covers Remove semantics.
func TestRegistry_RemoveIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	r := NewRegistryAt(path)

	require.NoError(t, r.Add("dev"))
	require.NoError(t, r.Add("gpu"))
	require.NoError(t, r.Remove("dev"))
	got, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"gpu"}, got)

	// Remove unknown is a no-op.
	require.NoError(t, r.Remove("ghost"))
	got, err = r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"gpu"}, got)
}

// TestRegistry_TouchMovesAliasToHead covers the MRU ordering contract.
func TestRegistry_TouchMovesAliasToHead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	r := NewRegistryAt(path)

	require.NoError(t, r.Add("dev"))
	require.NoError(t, r.Add("gpu"))
	require.NoError(t, r.Add("prod"))

	require.NoError(t, r.Touch("dev"))
	got, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"dev", "gpu", "prod"}, got)

	// Touch a middle entry.
	require.NoError(t, r.Touch("gpu"))
	got, err = r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"gpu", "dev", "prod"}, got)
}

func TestRegistry_TouchUnknownIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	r := NewRegistryAt(path)
	require.NoError(t, r.Add("dev"))

	require.NoError(t, r.Touch("ghost"))
	got, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"dev"}, got)
}

func TestRegistry_TouchLocalAliasIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.json")
	r := NewRegistryAt(path)
	require.NoError(t, r.Add("dev"))

	require.NoError(t, r.Touch(LocalAlias))
	got, err := r.List()
	require.NoError(t, err)
	assert.Equal(t, []string{"dev"}, got)
	assert.NotContains(t, got, LocalAlias)
}
// else → SSHHost bound to the alias. FromInstanceData relies on this.
func TestLookup(t *testing.T) {
	_, ok := Lookup(LocalAlias).(LocalHost)
	assert.True(t, ok, "local alias must resolve to LocalHost")

	_, ok = Lookup("").(LocalHost)
	assert.True(t, ok, "empty alias must resolve to LocalHost (cold start)")

	ssh, ok := Lookup("dev-machine").(SSHHost)
	require.True(t, ok, "non-local alias must resolve to SSHHost")
	assert.Equal(t, "dev-machine", ssh.Alias())
}
