// Package repo provides the storage layer for the set of repositories known
// to boulez (the "repo registry").
//
// The registry is a list of repository paths bound to a host. It is used to
// pre-populate the repo selector at instance creation. A path chosen freely at
// creation time (not previously registered) is added to the registry so it is
// offered the next time. The same path may be registered against several hosts
// (local and one or more ssh aliases) without clashing: a (path, host) pair is
// the dedup key, not the path alone.
//
// The host binding is stored (and used by the TUI to scope the selector), but
// it is NOT consulted at spawn time: the caller (TUI host selector, CLI --host)
// chooses the host and sends it on the wire authoritatively. The daemon never
// reads this registry. Keeping the registry a TUI convenience preserves the
// existing security model (caller chooses host; daemon trusts the wire) and
// the layering (repo is a storage layer; it does not import the host/transport
// package).
//
// On-disk format (v1):
//
//	{"version":1,"entries":[{"path":"/...","host":"local"}, ...]}
//
// A legacy flat `["/p1","/p2"]` file is migrated in place on first load: every
// bare path becomes a "local" entry, and the original bytes are backed up to
// repos.json.pre-migration (once). Insertion order is preserved.
package repo

import (
	"encoding/json"
	"fmt"
	"github.com/yro7/boulez/config"
	"os"
	"path/filepath"
)

// registryFileName is the name of the registry file inside the boulez config dir.
const registryFileName = "repos.json"

// localAlias is the canonical host value for the machine running boulez. It
// mirrors host.LocalAlias ("local"); repo does not import the host package
// (repo is a pure storage layer, host is the transport seam), so the literal
// is duplicated here on purpose. A change to host.LocalAlias MUST be mirrored
// here.
const localAlias = "local"

// Entry is one registered repo bound to a host. Host is an opaque alias
// ("local" for the machine running boulez, or an ssh alias like
// "dev-machine"). The same path on two hosts is two entries.
type Entry struct {
	Path string `json:"path"`
	Host string `json:"host"`
}

// registryV1 is the on-disk shape of the registry.
type registryV1 struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// registryVersion is the current on-disk schema version.
const registryVersion = 1

// Registry is the storage layer for known (repo, host) bindings. It is a deep
// module: a small surface (List/ListByHost/Add/Remove/Contains/Touch) over a
// persistent list that handles host-conditional path normalization,
// (path,host) deduplication, legacy migration, and round-tripping.
type Registry struct {
	// path is the filesystem location of the registry file. Injected so tests
	// can use an isolated temp file.
	path string
}

// NewRegistry returns a Registry backed by repos.json inside the boulez config
// directory (~/.boulez/). The directory is created on demand by config.GetConfigDir.
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

// load reads the persisted entries. A missing or corrupt file is treated as an
// empty registry (cold start / self-heal) rather than an error. A legacy flat
// `[]string` file is migrated to v1 in place, with a one-time backup of the
// original bytes written next to the file.
func (r *Registry) load() ([]Entry, error) {
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	// Try the current v1 shape first.
	var v1 registryV1
	if err := json.Unmarshal(data, &v1); err == nil && v1.Version == registryVersion {
		return normalizeHosts(v1.Entries), nil
	}

	// Legacy flat list of path strings. Migrate it in place.
	var flat []string
	if err := json.Unmarshal(data, &flat); err != nil {
		// Neither shape: corrupt file. Ignore it and start fresh, matching
		// the original "corrupt → empty" contract. No backup: there is nothing
		// trustable to back up.
		return nil, nil
	}
	entries := make([]Entry, 0, len(flat))
	for _, p := range flat {
		entries = append(entries, Entry{Path: p, Host: localAlias})
	}

	// Best-effort backup of the original bytes, once. A backup failure does
	// not block migration (the storage layer stays dependency-free and
	// best-effort, matching the existing tone). An existing backup is never
	// overwritten so a re-load after migration keeps the true original.
	backupPath := r.path + ".pre-migration"
	if _, statErr := os.Stat(backupPath); os.IsNotExist(statErr) {
		_ = os.WriteFile(backupPath, data, 0644)
	}

	// Persist the migrated shape so subsequent loads take the v1 path.
	_ = r.save(entries)
	return entries, nil
}

// normalizeHosts fills an empty Host with localAlias (defensive: a hand-edited
// or partially-migrated file may omit it). It does not touch non-empty hosts.
func normalizeHosts(entries []Entry) []Entry {
	for i := range entries {
		if entries[i].Host == "" {
			entries[i].Host = localAlias
		}
	}
	return entries
}

// save writes the entries atomically enough for a single-process app: marshal
// the v1 shape then write back to the file, creating parent dirs as needed.
func (r *Registry) save(entries []Entry) error {
	data, err := json.MarshalIndent(registryV1{Version: registryVersion, Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0755); err != nil {
		return err
	}
	return os.WriteFile(r.path, data, 0644)
}

// List returns the known entries. Order is MRU (most-recently-used first):
// selecting a repo via Touch moves its entry to the head. Entries never
// touched remain in insertion order after the touched ones.
func (r *Registry) List() ([]Entry, error) {
	return r.load()
}

// ListByHost returns the paths bound to the given host, in MRU order. An empty
// host is treated as "local" (mirrors host.Lookup("") → Local). It is the
// convenience the TUI repo selector uses to scope the offered repos to the
// chosen host.
func (r *Registry) ListByHost(host string) ([]string, error) {
	if host == "" {
		host = localAlias
	}
	entries, err := r.load()
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Host == host {
			paths = append(paths, e.Path)
		}
	}
	return paths, nil
}

// Contains reports whether the given (path, host) pair is in the registry.
// Path normalization is host-conditional: a local path is resolved to absolute
// (so a relative form matches an absolute add, mirroring LocalHost); a remote
// path is compared verbatim (mirroring SSHHost's passthrough, so a remote
// ~-relative or absolute path is not mangled). A failure to read the registry
// is treated as "not present".
func (r *Registry) Contains(path, host string) bool {
	if host == "" {
		host = localAlias
	}
	norm, ok := normalizeForHost(path, host)
	if !ok {
		return false
	}
	entries, err := r.load()
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Host == host && e.Path == norm {
			return true
		}
	}
	return false
}

// Add registers a (path, host) pair. The path is normalized for the host
// (absolute for local, verbatim for remote) and de-duplicated against the
// same (path, host) pair: adding an already-known binding is a no-op, but the
// same path on a different host is a distinct entry. Insertion order is
// preserved. An empty host is treated as "local".
func (r *Registry) Add(path, host string) error {
	if host == "" {
		host = localAlias
	}
	norm, ok := normalizeForHost(path, host)
	if !ok {
		return fmt.Errorf("repo: cannot resolve path %q", path)
	}
	entries, err := r.load()
	if err != nil {
		return err
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Host == host && e.Path == norm {
			return nil
		}
	}
	return r.save(append(entries, Entry{Path: norm, Host: host}))
}

// Remove unregisters a (path, host) pair. Removing an unknown binding is a
// no-op (idempotent). Insertion order of the remaining entries is preserved.
// Only persists when something actually changed, so a no-op Remove on a
// missing file does not create an empty one.
func (r *Registry) Remove(path, host string) error {
	if host == "" {
		host = localAlias
	}
	norm, ok := normalizeForHost(path, host)
	if !ok {
		return fmt.Errorf("repo: cannot resolve path %q", path)
	}
	entries, err := r.load()
	if err != nil {
		return err
	}
	kept := entries[:0]
	changed := false
	for _, e := range entries {
		if e.Host == host && e.Path == norm {
			changed = true
			continue
		}
		kept = append(kept, e)
	}
	if !changed {
		return nil
	}
	return r.save(kept)
}

// Touch moves the given (path, host) entry to the head of the registry
// (most-recently-used first), so it is offered at the top of the selector
// next time. A binding not in the registry is a no-op — use Add to register a
// new one first. Insertion order of the other entries is preserved. Only
// persists when something actually changed.
func (r *Registry) Touch(path, host string) error {
	if host == "" {
		host = localAlias
	}
	norm, ok := normalizeForHost(path, host)
	if !ok {
		return fmt.Errorf("repo: cannot resolve path %q", path)
	}
	entries, err := r.load()
	if err != nil {
		return err
	}
	idx := -1
	for i, e := range entries {
		if e.Host == host && e.Path == norm {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	kept := make([]Entry, 0, len(entries)-1)
	for i, e := range entries {
		if i != idx {
			kept = append(kept, e)
		}
	}
	return r.save(append([]Entry{{Path: norm, Host: host}}, kept...))
}

// normalizeForHost returns the stored form of path for the given host. A local
// path is resolved to an absolute, cleaned path (against the process working
// directory) so a stored path survives a cwd change — mirroring
// LocalHost.ResolveRepoPath. A remote path is returned cleaned but otherwise
// verbatim, mirroring SSHHost.ResolveRepoPath (passthrough): resolving it
// locally would point at a path on the wrong machine and would mangle
// ~-relative or remote-absolute paths. The boolean is false when local
// resolution fails.
func normalizeForHost(path, host string) (string, bool) {
	if host == localAlias {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", false
		}
		return abs, true
	}
	return filepath.Clean(path), true
}
