package fs

import "os"

// LocalFS implements FS by delegating to the os package. This is the
// behaviour boulez has always had; it is the default for all v1 code paths.
// v2 adds a remote FS implementation alongside this one.
type LocalFS struct{}

// Stat implements FS.
func (LocalFS) Stat(name string) (os.FileInfo, error) { return os.Stat(name) }

// RemoveAll implements FS.
func (LocalFS) RemoveAll(path string) error { return os.RemoveAll(path) }

// MkdirAll implements FS.
func (LocalFS) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

// ReadDir implements FS.
func (LocalFS) ReadDir(name string) ([]os.DirEntry, error) { return os.ReadDir(name) }
