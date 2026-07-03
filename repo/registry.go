// Package repo provides the storage layer for the set of repositories known
// to cs2 (the "repo registry").
//
// The registry is a minimal, permissive list of repository paths. It is used to
// pre-populate the repo selector at instance creation. A path chosen freely at
// creation time (not previously registered) is added to the registry so it is
// offered the next time.
//
// The format is intentionally minimal: a JSON array of absolute path strings.
// No aliases, no default, no ordering metadata — those enrichments are deferred
// (see roadmap_and_ideas.md). Insertion order is preserved.
package repo

import (
	"claude-squad/config"
	"encoding/json"
	"os"
	"path/filepath"
)

// registryFileName is the name of the registry file inside the cs2 config dir.
const registryFileName = "repos.json"

// Registry is the storage layer for known repository paths. It is a deep
// module: a small surface (List/Add/Remove/Contains) over a persistent list
// that handles absolute-path resolution, deduplication and round-tripping.
type Registry struct {
	// path is the filesystem location of the registry file. Injected so tests
	// can use an isolated temp file.
	path string
}

// NewRegistry returns a Registry backed by repos.json inside the cs2 config
// directory (~/.cs2/). The directory is created on demand by config.GetConfigDir.
func NewRegistry() (*Registry, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return nil, err
	}
	return NewRegistryAt(filepath.Join(configDir, registryFileName)), nil
}

// NewRegistryAt returns a Registry backed by an explicit file path. Useful for
// tests and for directing the registry at a non-default location.
func NewRegistryAt(path string) *Registry {
	return &Registry{path: path}
}

// Path returns the filesystem location of the registry file.
func (r *Registry) Path() string {
	return r.path
}

// load reads the persisted paths. A missing or corrupt file is treated as an
// empty registry (cold start / self-heal) rather than an error.
func (r *Registry) load() ([]string, error) {
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	if err := json.Unmarshal(data, &paths); err != nil {
		// Corrupt file: ignore it and start fresh.
		return nil, nil
	}
	return paths, nil
}

// save writes the paths atomically enough for a single-process app: marshal
// then write back to the file, creating parent dirs as needed.
func (r *Registry) save(paths []string) error {
	data, err := json.MarshalIndent(paths, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0755); err != nil {
		return err
	}
	return os.WriteFile(r.path, data, 0644)
}

// List returns the known repository paths in stable insertion order.
func (r *Registry) List() ([]string, error) {
	return r.load()
}

// Contains reports whether the given path (resolved to absolute) is in the
// registry. A failure to read the registry is treated as "not present".
func (r *Registry) Contains(path string) bool {
	abs, err := resolveAbsolute(path)
	if err != nil {
		return false
	}
	paths, err := r.load()
	if err != nil {
		return false
	}
	for _, p := range paths {
		if p == abs {
			return true
		}
	}
	return false
}

// Add registers a repository path. The path is resolved to an absolute path
// and de-duplicated: adding an already-known path (in any form that resolves
// to the same absolute path) is a no-op. Insertion order is preserved.
func (r *Registry) Add(path string) error {
	abs, err := resolveAbsolute(path)
	if err != nil {
		return err
	}
	paths, err := r.load()
	if err != nil {
		return err
	}
	for _, p := range paths {
		if p == abs {
			return nil
		}
	}
	return r.save(append(paths, abs))
}

// Remove unregisters a repository path. The path is resolved to absolute
// first; removing an unknown path is a no-op (idempotent). Insertion order of
// the remaining entries is preserved.
func (r *Registry) Remove(path string) error {
	abs, err := resolveAbsolute(path)
	if err != nil {
		return err
	}
	paths, err := r.load()
	if err != nil {
		return err
	}
	kept := paths[:0]
	for _, p := range paths {
		if p != abs {
			kept = append(kept, p)
		}
	}
	// Only persist if something actually changed, so a no-op Remove on a
	// missing file does not create an empty one.
	if len(kept) == len(paths) {
		return nil
	}
	return r.save(kept)
}

// resolveAbsolute returns the absolute, cleaned form of path. Relative paths
// are resolved against the process working directory.
func resolveAbsolute(path string) (string, error) {
	return filepath.Abs(path)
}
