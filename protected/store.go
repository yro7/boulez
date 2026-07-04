// Package protected is the persistent store for per-repo protected-branch
// declarations. It is the daemon's replacement for the cwd-derived host
// branch (Phase 2 of the hierarchy inversion): the daemon has no cwd and no
// repo of its own, so the branches a merge may never target are declared
// explicitly per repo and persisted here, then read at daemon boot and on
// SIGHUP.
//
// The on-disk shape is per-repo (repoPath -> []branch) so the user manages
// protection at the granularity of a repository. Runtime enforcement in the
// kernel is a flat, kernel-wide guard (the kernel refuses a protected branch
// name for any repo): this is intentionally conservative — a branch protected
// for any repo is refused for all repos. The conventional main/master guard
// in the Merger remains as defense in depth; this store is the authoritative,
// non-contournable layer for declared protected branches.
package protected

import (
	"claude-squad/config"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Store is a deep module: a small surface (Load/Add/Remove/Flat) over a
// per-repo protected-branch map persisted to ~/.cs2/protected.json. It is
// the single source of truth for declared protected branches. Both the daemon
// (boot + SIGHUP reload) and the `cs2 daemon protect|unprotect|list-protected`
// commands operate through it.
type Store struct {
	// path is the filesystem location of the store file. Injected so tests
	// can use an isolated temp file.
	path string
}

// FileName is the store file name inside the cs2 config dir.
const FileName = "protected.json"

// New returns a Store backed by protected.json inside the cs2 config dir
// (~/.cs2/). The directory is created on demand by config.GetConfigDir.
func New() (*Store, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return nil, err
	}
	return NewAt(filepath.Join(dir, FileName)), nil
}

// NewAt returns a Store backed by an explicit file path. Useful for tests
// and for directing the store at a non-default location.
func NewAt(path string) *Store {
	return &Store{path: path}
}

// Path returns the filesystem location of the store file.
func (s *Store) Path() string { return s.path }

// Load reads the persisted per-repo protected branches. A missing or empty
// file is a valid cold start and yields an empty (non-nil) map. A corrupt
// file is treated as empty (self-heal) rather than erroring, so a bad
// protected.json never blocks the daemon from booting.
func (s *Store) Load() (map[string][]string, error) {
	out := map[string][]string{}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("read protected store %s: %w", s.path, err)
	}
	if len(data) == 0 {
		return out, nil
	}
	var raw map[string][]string
	if err := json.Unmarshal(data, &raw); err != nil {
		// Corrupt: ignore and start fresh. Defense in depth — the daemon must
		// still boot, and the conventional main/master guard still applies.
		return out, nil
	}
	for repo, branches := range raw {
		seen := map[string]bool{}
		clean := make([]string, 0, len(branches))
		for _, b := range branches {
			if b == "" || seen[b] {
				continue
			}
			seen[b] = true
			clean = append(clean, b)
		}
		out[repo] = clean
	}
	return out, nil
}

// Add declares a branch protected for the given repo. The repo path is
// resolved to absolute (so the same repo is identified regardless of where
// the command runs). Adding an already-protected branch is a no-op.
func (s *Store) Add(repo, branch string) error {
	if branch == "" {
		return fmt.Errorf("protected: empty branch name")
	}
	abs, err := resolveAbsolute(repo)
	if err != nil {
		return err
	}
	all, err := s.Load()
	if err != nil {
		return err
	}
	for _, b := range all[abs] {
		if b == branch {
			return nil
		}
	}
	all[abs] = append(all[abs], branch)
	return s.save(all)
}

// Remove unprotects a branch for the given repo. Removing a branch that was
// not protected is a no-op (idempotent). The repo path is resolved to
// absolute first.
func (s *Store) Remove(repo, branch string) error {
	abs, err := resolveAbsolute(repo)
	if err != nil {
		return err
	}
	all, err := s.Load()
	if err != nil {
		return err
	}
	branches := all[abs]
	kept := branches[:0]
	changed := false
	for _, b := range branches {
		if b == branch {
			changed = true
			continue
		}
		kept = append(kept, b)
	}
	if !changed {
		return nil
	}
	if len(kept) == 0 {
		delete(all, abs)
	} else {
		all[abs] = kept
	}
	return s.save(all)
}

// Flat returns the union of all protected branch names across all repos. This
// is the shape the kernel consumes: a branch protected for any repo is
// refused kernel-wide (conservative, fails closed). The result is sorted and
// de-duplicated so it is stable across reloads. Used by the daemon to feed
// kernel.WithProtectedBranches (at boot) and kernel.SetProtectedBranches (on
// SIGHUP).
func (s *Store) Flat() ([]string, error) {
	all, err := s.Load()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, branches := range all {
		for _, b := range branches {
			seen[b] = true
		}
	}
	out := make([]string, 0, len(seen))
	for b := range seen {
		out = append(out, b)
	}
	sort.Strings(out)
	return out, nil
}

// save writes the per-repo map atomically enough for a single-writer app:
// marshal then write back to the file, creating parent dirs as needed.
func (s *Store) save(all map[string][]string) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return fmt.Errorf("create protected store dir: %w", err)
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal protected store: %w", err)
	}
	return os.WriteFile(s.path, data, 0644)
}

// resolveAbsolute returns the absolute, cleaned form of path. Relative paths
// are resolved against the process working directory. Mirrors repo.Registry
// so a repo is identified the same way in both stores.
func resolveAbsolute(path string) (string, error) {
	return filepath.Abs(path)
}
