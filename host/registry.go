// Package host provides the storage layer for the set of ssh hosts known to
// cs2 (the "host registry").
//
// The registry is a minimal, permissive list of ssh aliases. It is used to
// pre-populate the host selector at instance creation. An alias typed freely
// at creation time (not previously registered) is added to the registry so it
// is offered the next time. The "local" host is always available implicitly
// and is never stored in the registry.
//
// The format is intentionally minimal: a JSON array of alias strings.
// Mirrors repo.Registry's design (PLAN-multi-repo.md). No enrichments
// (default, ordering metadata) — those are deferred (see roadmap_and_ideas.md).
package host

import (
	"claude-squad/config"
	"encoding/json"
	"os"
	"path/filepath"
)

// registryFileName is the name of the registry file inside the cs2 config dir.
const registryFileName = "hosts.json"

// LocalAlias is the canonical alias for the local host (the machine running
// cs2). Stored in InstanceData.Host when an instance runs locally; never
// stored in the registry (it is always available implicitly).
const LocalAlias = "local"

// Registry is the storage layer for known ssh aliases. Deep module: a small
// surface (List/Add/Remove/Contains) over a persistent list that handles
// deduplication and round-tripping.
type Registry struct {
	// path is the filesystem location of the registry file. Injected so tests
	// can use an isolated temp file.
	path string
}

// NewRegistry returns a Registry backed by hosts.json inside the cs2 config
// directory (~/.cs2/).
func NewRegistry() (*Registry, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return nil, err
	}
	return NewRegistryAt(filepath.Join(configDir, registryFileName)), nil
}

// NewRegistryAt returns a Registry backed by an explicit file path.
func NewRegistryAt(path string) *Registry {
	return &Registry{path: path}
}

// Path returns the filesystem location of the registry file.
func (r *Registry) Path() string { return r.path }

// load reads the persisted aliases. A missing or corrupt file is treated as
// an empty registry (cold start / self-heal) rather than an error.
func (r *Registry) load() ([]string, error) {
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var aliases []string
	if err := json.Unmarshal(data, &aliases); err != nil {
		// Corrupt file: ignore it and start fresh.
		return nil, nil
	}
	return aliases, nil
}

// save writes the aliases atomically enough for a single-process app.
func (r *Registry) save(aliases []string) error {
	data, err := json.MarshalIndent(aliases, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0755); err != nil {
		return err
	}
	return os.WriteFile(r.path, data, 0644)
}

// List returns the known ssh aliases. Order is MRU (most-recently-used first):
// selecting an alias via Touch moves it to the head. Entries never touched
// remain in insertion order after the touched ones. LocalAlias is never
// present (it is always available implicitly).
func (r *Registry) List() ([]string, error) {
	return r.load()
}

// Contains reports whether the given alias is in the registry.
func (r *Registry) Contains(alias string) bool {
	aliases, err := r.load()
	if err != nil {
		return false
	}
	for _, a := range aliases {
		if a == alias {
			return true
		}
	}
	return false
}

// Add registers an ssh alias. De-duplicated: adding an already-known alias is
// a no-op. LocalAlias is rejected (it is reserved for the implicit local
// host). Insertion order is preserved.
func (r *Registry) Add(alias string) error {
	if alias == "" || alias == LocalAlias {
		return nil
	}
	aliases, err := r.load()
	if err != nil {
		return err
	}
	for _, a := range aliases {
		if a == alias {
			return nil
		}
	}
	return r.save(append(aliases, alias))
}

// Touch moves the given alias to the head of the registry (most-recently-used
// first), so it is offered at the top of the selector next time. An alias not
// in the registry is a no-op — use Add to register a new alias first.
// LocalAlias is never stored, so touching it is a no-op. Insertion order of
// the other entries is preserved. Only persists when something actually
// changed.
func (r *Registry) Touch(alias string) error {
	if alias == "" || alias == LocalAlias {
		return nil
	}
	aliases, err := r.load()
	if err != nil {
		return err
	}
	idx := -1
	for i, a := range aliases {
		if a == alias {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	kept := make([]string, 0, len(aliases)-1)
	for i, a := range aliases {
		if i != idx {
			kept = append(kept, a)
		}
	}
	return r.save(append([]string{alias}, kept...))
}

// Remove unregisters an ssh alias. Removing an unknown alias is a no-op
// (idempotent). Insertion order of the remaining entries is preserved.
func (r *Registry) Remove(alias string) error {
	aliases, err := r.load()
	if err != nil {
		return err
	}
	kept := aliases[:0]
	for _, a := range aliases {
		if a != alias {
			kept = append(kept, a)
		}
	}
	if len(kept) == len(aliases) {
		return nil
	}
	return r.save(kept)
}
