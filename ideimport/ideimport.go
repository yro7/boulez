package ideimport

import (
	"fmt"
	"os"
	"path/filepath"
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
