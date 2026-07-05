// Package prefs stores per-repo preferences that boulez learns to apply at instance
// creation. Today the only preference is repo → profile: which program profile
// to preselect in the prompt overlay when creating an instance in a given repo.
//
// The store is minimal and self-healing: a JSON object keyed by absolute repo
// path, value a small struct. A missing or corrupt file is treated as empty,
// never an error that blocks startup. The format mirrors the registries
// (repo/host): a dedicated package, SRP — prefs knows about preferences and
// nothing else.
package prefs

import (
	"encoding/json"
	"fmt"
	"github.com/yro7/boulez/config"
	"os"
	"path/filepath"
)

// Preference is a single per-repo preference. Today only Profile is set; future
// fields (branch, prompt template) extend this struct without breaking the
// file format (unknown fields are ignored by json.Unmarshal).
type Preference struct {
	// Profile is the name of the config.Profile to preselect for this repo.
	// Empty means "no preference" (the default profile is used).
	Profile string `json:"profile,omitempty"`
	// Program is the resolved program string of the profile, cached so the
	// caller can apply the preference without re-resolving from config. Kept
	// in sync with Profile by the setter.
	Program string `json:"program,omitempty"`
}

// Store is the persistent repo→preference store. Deep module: a small surface
// (Get/Set/Clear) over a JSON map keyed by absolute repo path.
type Store struct {
	// path is the filesystem location of the preferences file. Injected so
	// tests can use an isolated temp file.
	path string
}

const storeFileName = "preferences.json"

// NewStore returns a Store backed by preferences.json inside the boulez config
// directory (~/.boulez/). The directory is created on demand by config.GetConfigDir.
func NewStore() (*Store, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return nil, err
	}
	return NewStoreAt(filepath.Join(configDir, storeFileName)), nil
}

// NewStoreAt returns a Store backed by an explicit file path. Useful for tests.
func NewStoreAt(path string) *Store {
	return &Store{path: path}
}

// Path returns the filesystem location of the preferences file.
func (s *Store) Path() string { return s.path }

// load reads the persisted preferences. A missing or corrupt file is treated
// as an empty store (cold start / self-heal) rather than an error.
func (s *Store) load() (map[string]Preference, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Preference{}, nil
		}
		return nil, err
	}
	var m map[string]Preference
	if err := json.Unmarshal(data, &m); err != nil {
		// Corrupt file: ignore it and start fresh.
		return map[string]Preference{}, nil
	}
	return m, nil
}

// save writes the preferences atomically enough for a single-process app.
func (s *Store) save(m map[string]Preference) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

// Get returns the preference for repoPath (resolved to absolute). The bool is
// false when no preference is set for this repo.
func (s *Store) Get(repoPath string) (Preference, bool, error) {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return Preference{}, false, err
	}
	m, err := s.load()
	if err != nil {
		return Preference{}, false, err
	}
	p, ok := m[abs]
	return p, ok, nil
}

// Set records a profile preference for repoPath (resolved to absolute). The
// program string is stored alongside the name so callers can apply the
// preference without re-resolving from config.
func (s *Store) Set(repoPath, profileName, program string) error {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	m, err := s.load()
	if err != nil {
		return err
	}
	m[abs] = Preference{Profile: profileName, Program: program}
	return s.save(m)
}

// Clear removes any preference for repoPath (resolved to absolute). Idempotent.
func (s *Store) Clear(repoPath string) error {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	m, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := m[abs]; !ok {
		return nil
	}
	delete(m, abs)
	return s.save(m)
}

// String returns a debug description of the store (path + entry count).
func (s *Store) String() string {
	m, err := s.load()
	if err != nil {
		return fmt.Sprintf("prefs.Store(%s): <unreadable: %v>", s.path, err)
	}
	return fmt.Sprintf("prefs.Store(%s): %d preferences", s.path, len(m))
}
