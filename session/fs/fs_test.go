package fs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLocalFS_MirrorsOs verifies that LocalFS behaves identically to the os
// package for the operations cs2 relies on. This is the contract v2's remote
// FS must preserve (modulo acting on the right host).
func TestLocalFS_MirrorsOs(t *testing.T) {
	lfs := LocalFS{}
	root := t.TempDir()

	// MkdirAll: nested dirs created with the given perm.
	sub := filepath.Join(root, "a", "b")
	require.NoError(t, lfs.MkdirAll(sub, 0o755))

	// Stat: reports the dir created above.
	info, err := lfs.Stat(sub)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// Stat on a missing path → an error os.IsNotExist recognises (Pause relies
	// on this to detect orphaned worktrees).
	_, err = lfs.Stat(filepath.Join(root, "nope"))
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err), "missing path must satisfy os.IsNotExist")

	// ReadDir: lists entries written under root.
	require.NoError(t, os.WriteFile(filepath.Join(root, "f1"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "f2"), []byte("y"), 0o644))
	entries, err := lfs.ReadDir(root)
	require.NoError(t, err)
	assert.Len(t, entries, 3) // a/, f1, f2

	// RemoveAll: removes a tree (the worktree dir, including children).
	require.NoError(t, lfs.RemoveAll(filepath.Join(root, "a")))
	_, err = lfs.Stat(sub)
	assert.True(t, os.IsNotExist(err), "RemoveAll must delete the tree")
}
