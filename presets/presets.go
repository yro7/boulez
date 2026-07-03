// Package presets stores named "quick session" presets: complete, explicit
// recipes for starting an instance with zero selectors. A preset hardcodes
// host + repo + profile + prompt + branch, so a single Ctrl+R → pick → name
// launches a standard session without walking the host/repo/prompt overlays.
//
// The store mirrors the design of prefs/repo/host stores: a dedicated package
// (SRP — presets knows about presets and nothing else), a minimal JSON format,
// and self-healing (a missing or corrupt file is treated as empty, never an
// error that blocks startup). The file is read fresh on every call, so an
// agent or editor can change it between two Ctrl+R opens and cs2 picks up the
// new contents with no watcher (read-on-open reload — see PLAN-quick-session).
//
// A preset is an explicit, complete recipe: it does NOT reference the
// repo→profile preference store. If you want the magic of "use my preferred
// profile for this repo", use the normal N flow (which preselects via prefs).
// Presets trade that flexibility for full reproducibility.
package presets

import (
	"claude-squad/config"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// storeFileName is the name of the presets file inside the cs2 config dir.
const storeFileName = "presets.json"

// Preset is a complete recipe for starting an instance without the selector
// flow. Every field is explicit; there is no indirection through the registries
// or the prefs store. Unknown JSON fields are ignored by json.Unmarshal, so
// future fields extend this struct without breaking the file format.
type Preset struct {
	// Repo is the path to the git repository the instance works in. Required.
	// Resolved to an absolute path on Set.
	Repo string `json:"repo"`
	// Host is the execution host: "local" (default when empty) or an ssh alias.
	// An alias not in the host registry is still usable (Lookup constructs an
	// SSHHost regardless); the preset does not mutate the registry.
	Host string `json:"host,omitempty"`
	// Profile is the name of a config.Profile whose Program is used. Empty means
	// "use the default program" (the cs2 --program flag / config default). A
	// name that matches no profile is rejected at selection time, not here.
	Profile string `json:"profile,omitempty"`
	// Prompt is the initial task sent to the agent once it has started. Empty
	// means no initial prompt (the instance starts in Ready).
	Prompt string `json:"prompt,omitempty"`
	// Branch is the existing branch to start on. Empty means a new branch from
	// HEAD (the cs2/<title> convention). A name that does not exist is rejected
	// at Start time (the preset does not create branches).
	Branch string `json:"branch,omitempty"`
}

// Store is the persistent named-preset store. Deep module: a small surface
// (List/Get/Set/Remove) over a JSON map keyed by preset name.
type Store struct {
	// path is the filesystem location of the presets file. Injected so tests
	// can use an isolated temp file.
	path string
}

// NewStore returns a Store backed by presets.json inside the cs2 config
// directory (~/.cs2/). The directory is created on demand by config.GetConfigDir.
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

// Path returns the filesystem location of the presets file.
func (s *Store) Path() string { return s.path }

// load reads the persisted presets. A missing or corrupt file is treated as
// an empty store (cold start / self-heal) rather than an error.
func (s *Store) load() (map[string]Preset, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]Preset{}, nil
		}
		return nil, err
	}
	var m map[string]Preset
	if err := json.Unmarshal(data, &m); err != nil {
		// Corrupt file: ignore it and start fresh.
		return map[string]Preset{}, nil
	}
	return m, nil
}

// save writes the presets atomically enough for a single-process app.
func (s *Store) save(m map[string]Preset) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0644)
}

// List returns the preset names in stable (alphabetical) order. A failure to
// read the file is treated as an empty store.
func (s *Store) List() ([]string, error) {
	m, err := s.load()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Get returns the preset for the given name. The bool is false when no preset
// exists with that name.
func (s *Store) Get(name string) (Preset, bool, error) {
	m, err := s.load()
	if err != nil {
		return Preset{}, false, err
	}
	p, ok := m[name]
	return p, ok, nil
}

// Set records a preset under the given name, overwriting any existing preset
// with the same name. The repo path is resolved to an absolute path so a preset
// is portable regardless of how it was authored.
func (s *Store) Set(name string, p Preset) error {
	if name == "" {
		return fmt.Errorf("preset name cannot be empty")
	}
	abs, err := filepath.Abs(p.Repo)
	if err != nil {
		return fmt.Errorf("resolve repo path: %w", err)
	}
	p.Repo = abs
	m, err := s.load()
	if err != nil {
		return err
	}
	if m == nil {
		m = map[string]Preset{}
	}
	m[name] = p
	return s.save(m)
}

// Remove deletes the preset with the given name. Idempotent: removing an
// unknown name is a no-op.
func (s *Store) Remove(name string) error {
	m, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := m[name]; !ok {
		return nil
	}
	delete(m, name)
	return s.save(m)
}

// String returns a debug description of the store (path + entry count).
func (s *Store) String() string {
	m, err := s.load()
	if err != nil {
		return fmt.Sprintf("presets.Store(%s): <unreadable: %v>", s.path, err)
	}
	return fmt.Sprintf("presets.Store(%s): %d presets", s.path, len(m))
}
