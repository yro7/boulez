package ideimport

import (
	"claude-squad/session/git"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ideSpec describes one VS Code-family IDE whose globalStorage/storage.json we
// scan. All fields are unexported: this is an internal detail. The display
// name (name) is the only value that ever leaves the package, via FoundRepo.
type ideSpec struct {
	name    string // human-readable, e.g. "VS Code", "Antigravity"
	dirName string // subfolder under the OS-specific config root
	canon   string // canonical --ide flag value, e.g. "vscode", "antigravity"
}

// knownIDEs is the fixed list of VS Code-family IDEs supported by the import.
// Each is a fork of the VS Code runtime and therefore shares the same
// globalStorage/storage.json layout. Folder names for PearAI, Void and Trae
// are best-effort (not verified on a machine that has them installed); a
// wrong folder name is harmless — the IDE is simply absent on disk and
// skipped silently (D14).
var knownIDEs = []ideSpec{
	{"VS Code", "Code", "vscode"},
	{"Cursor", "Cursor", "cursor"},
	{"Windsurf", "Windsurf", "windsurf"},
	{"Antigravity", "Antigravity IDE", "antigravity"},
	{"VSCodium", "VSCodium", "vscodium"},
	{"PearAI", "PearAI", "pearai"},
	{"Void", "Void", "void"},
	{"Trae", "Trae", "trae"},
}

// lookupIDE resolves a canonical IDE name (the --ide flag value) to its spec.
// Returns ok=false if the name is not a known IDE. Used to validate the
// --ide flag before any scan (fail-fast, D12).
func lookupIDE(name string) (ideSpec, bool) {
	for _, spec := range knownIDEs {
		if spec.canon == name {
			return spec, true
		}
	}
	return ideSpec{}, false
}

// validIDENames returns the canonical names of all known IDEs as a
// comma-separated string, for inclusion in error messages.
func validIDENames() string {
	names := make([]string, len(knownIDEs))
	for i, s := range knownIDEs {
		names[i] = s.canon
	}
	return strings.Join(names, ", ")
}

// configRoot returns the OS-specific directory under which a VS Code-family
// IDE stores its globalStorage. goos is parameterised (rather than always
// reading runtime.GOOS) so each OS branch is unit-testable on any host.
//
// macOS is the only platform covered by tests (plan §7); the linux and
// windows branches are present so no OS-specific path is ever hardcoded in
// the scan logic — the seam exists even where coverage does not.
func configRoot(goos, homeDir string) (string, error) {
	switch goos {
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support"), nil
	case "linux":
		return filepath.Join(homeDir, ".config"), nil
	case "windows":
		// APPDATA is the conventional root; fall back to a homeDir-relative
		// path if unset. Windows is not test-covered but the branch is here
		// so the path is never buried in scan logic.
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return appdata, nil
		}
		return filepath.Join(homeDir, "AppData", "Roaming"), nil
	default:
		return "", fmt.Errorf("ideimport: unsupported OS: %s", goos)
	}
}

// storagePath returns the path to an IDE's globalStorage/storage.json under
// the given home directory. goos is parameterised for testability (see
// configRoot).
func storagePath(goos, homeDir string, spec ideSpec) (string, error) {
	root, err := configRoot(goos, homeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, spec.dirName, "User", "globalStorage", "storage.json"), nil
}

// FoundRepo is a repository path discovered in an IDE's state, plus the
// display name of the source IDE (for the import summary).
type FoundRepo struct {
	Path string // absolute filesystem path, decoded from a file:// URL
	IDE  string // display name, e.g. "Antigravity", "Cursor"
}

// Warning reports a non-fatal problem encountered while scanning a single
// IDE (e.g. a corrupt storage.json). Scan continues with the remaining IDEs;
// the caller surfaces these to the user (D16).
type Warning struct {
	IDE   string // display name
	Cause string // human-readable reason
}

// Importer scans VS Code-family IDE state files and reports the git
// repositories found among their recently-opened folders. It does NOT touch
// the cs2 registry — Scan returns discoveries; the caller does Registry.Add.
// This keeps the package testable without a registry and reusable for
// --dry-run (D19).
type Importer struct {
	homeDir string // injected so tests use a temp HOME, never the real one
	ideName string // "" = all known IDEs; canonical name = restrict to one
}

// NewImporter returns an Importer over the real user home directory. name is
// "" (scan all IDEs) or a canonical IDE name to restrict to one. An invalid
// name yields an error before any scan (fail-fast, D12).
func NewImporter(name string) (*Importer, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return NewImporterAt(home, name)
}

// NewImporterAt returns an Importer bound to an explicit homeDir (for tests).
// A non-empty name must be a valid canonical IDE name (validated here, D12).
func NewImporterAt(homeDir, name string) (*Importer, error) {
	if name != "" {
		if _, ok := lookupIDE(name); !ok {
			return nil, fmt.Errorf("ideimport: unknown IDE %q; valid: %s", name, validIDENames())
		}
	}
	return &Importer{homeDir: homeDir, ideName: name}, nil
}

// Scan reads each applicable IDE's storage.json, collects file:// URLs,
// decodes them to absolute paths, keeps those that are git repositories, and
// returns them deduplicated by absolute path (D9). IDEs whose storage.json is
// missing are silently skipped (D15); a present-but-unreadable or corrupt
// file yields a Warning for that IDE (D16) but does not abort the scan.
// Returns an error only for hard failures (e.g. unsupported OS), never for
// "no IDE found" (D17).
func (i *Importer) Scan() ([]FoundRepo, []Warning, error) {
	specs := i.specsToScan()
	var found []FoundRepo
	var warnings []Warning
	seen := make(map[string]struct{})
	for _, spec := range specs {
		repos, warns, err := i.scanOne(spec)
		warnings = append(warnings, warns...)
		if err != nil {
			return nil, warnings, err // hard failure aborts
		}
		for _, r := range repos {
			if _, ok := seen[r.Path]; !ok {
				seen[r.Path] = struct{}{}
				found = append(found, r)
			}
		}
	}
	return found, warnings, nil
}

// specsToScan returns the IDE specs this importer will scan: all known IDEs,
// or just the one named by --ide when ideName is set (validated in the
// constructor, so lookupIDE here is defensive).
func (i *Importer) specsToScan() []ideSpec {
	if i.ideName != "" {
		if spec, ok := lookupIDE(i.ideName); ok {
			return []ideSpec{spec}
		}
	}
	return knownIDEs
}

// scanOne scans a single IDE's storage.json. Returns the discovered git repos
// (not yet cross-IDE deduped). A missing storage.json is a silent skip (no
// repos, no warning, no error — D15). A present-but-unreadable or
// JSON-corrupt file yields a warning (D16) but no error.
func (i *Importer) scanOne(spec ideSpec) ([]FoundRepo, []Warning, error) {
	path, err := storagePath(runtime.GOOS, i.homeDir, spec)
	if err != nil {
		return nil, nil, err // unsupported OS = hard failure
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil // D15: silent skip
		}
		// Present but unreadable (permissions, etc.) → warning, not fatal.
		return nil, []Warning{{IDE: spec.name, Cause: err.Error()}}, nil
	}
	var node any
	if err := json.Unmarshal(data, &node); err != nil {
		// Corrupt JSON → warning (D16).
		return nil, []Warning{{IDE: spec.name, Cause: err.Error()}}, nil
	}
	var found []FoundRepo
	for _, u := range collectPaths(node) {
		p, ok := decodeFileURL(u)
		if !ok {
			continue
		}
		if git.IsGitRepo(p) {
			found = append(found, FoundRepo{Path: p, IDE: spec.name})
		}
	}
	return found, nil, nil
}
