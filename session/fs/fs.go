// Package fs abstracts filesystem operations behind an injectable interface.
//
// The git and session packages operate on worktree paths that are assumed to
// live on the local filesystem. For a local-only boulez this is correct, but an
// instance whose worktree lives on a remote host cannot be manipulated with
// os.Stat / os.RemoveAll — those act on the local machine, silently doing the
// wrong thing (a local stat of a remote path is a bug, not a network error).
//
// FS is the seam: LocalFS delegates to the os package (today's behaviour);
// v2 adds a remote implementation that runs the same operations over SSH.
// Callers depend on FS, not on os, so the transport is swappable.
package fs

import (
	"os"
)

// FS is the filesystem surface boulez needs against worktree paths. Kept narrow
// on purpose — only the operations the git/session layer actually uses.
type FS interface {
	// Stat reports metadata for the named path. Mirrors os.Stat.
	Stat(name string) (os.FileInfo, error)
	// RemoveAll removes path and any children it contains. Mirrors
	// os.RemoveAll.
	RemoveAll(path string) error
	// MkdirAll creates path (and parents) with the given permission bits.
	// Mirrors os.MkdirAll.
	MkdirAll(path string, perm os.FileMode) error
	// ReadDir lists the entries of a directory. Mirrors os.ReadDir.
	ReadDir(name string) ([]os.DirEntry, error)
}
