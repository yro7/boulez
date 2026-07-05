package host

import (
	"os"
	"time"
)

// minimalFileInfo is a minimal os.FileInfo for remote paths. boulez's callers
// (Pause, Cleanup, IsValidWorktree) only rely on existence and the
// os.IsNotExist sentinel; IsDir is included for completeness. Size/ModTime
// are not fetched over the network (no caller needs them).
type minimalFileInfo struct {
	name  string
	isDir bool
}

func (m minimalFileInfo) Name() string       { return m.name }
func (m minimalFileInfo) Size() int64        { return 0 }
func (m minimalFileInfo) Mode() os.FileMode  { return 0 }
func (m minimalFileInfo) ModTime() time.Time { return time.Time{} }
func (m minimalFileInfo) IsDir() bool        { return m.isDir }
func (m minimalFileInfo) Sys() interface{}   { return nil }

// dirEntry is a minimal os.DirEntry for remote directory listings.
type dirEntry struct {
	name string
}

func (d dirEntry) Name() string               { return d.name }
func (d dirEntry) IsDir() bool                { return false }
func (d dirEntry) Type() os.FileMode          { return 0 }
func (d dirEntry) Info() (os.FileInfo, error) { return minimalFileInfo{name: d.name}, nil }

// errNotExist returns an error satisfying os.IsNotExist, matching the
// contract LocalFS (os.Stat) gives the callers that detect orphaned worktrees.
func errNotExist(name string) error {
	return os.ErrNotExist
}
