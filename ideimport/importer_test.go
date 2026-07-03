package ideimport

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// antigravitySpec mirrors the single IDE hardcoded by specsToScan in commit 3a.
var antigravitySpec = ideSpec{name: "Antigravity", dirName: "Antigravity IDE", canon: "antigravity"}

// makeGitRepo creates a real git repository at dir so git.IsGitRepo returns
// true for it. Mirrors app/app_test.go's makeTestRepo, trimmed to the minimum.
func makeGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %s: %s", args, out)
	}
	require.NoError(t, os.MkdirAll(dir, 0755))
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0644))
	run("add", ".")
	run("commit", "-m", "initial")
}

// fileURL builds a correctly percent-encoded file:// URL for a path.
func fileURL(p string) string {
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// writeStorage writes the given storage.json body into the Antigravity IDE
// globalStorage dir under homeDir, creating parents as needed.
func writeStorage(t *testing.T, homeDir, body string) {
	writeStorageFor(t, homeDir, antigravitySpec, body)
}

// writeStorageFor writes a storage.json body for an arbitrary IDE spec.
func writeStorageFor(t *testing.T, homeDir string, spec ideSpec, body string) {
	t.Helper()
	p, err := storagePath("darwin", homeDir, spec)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0755))
	require.NoError(t, os.WriteFile(p, []byte(body), 0644))
}

// storageBody builds a minimal VS Code-family storage.json that references
// the given file:// URLs in backupWorkspaces.folders and (for the first URL)
// also in windowsState.lastActiveWindow, to exercise the recursive walker and
// dedup. Sensitive-looking keys are placeholders only.
func storageBody(urls ...string) string {
	folders := ""
	for i, u := range urls {
		if i > 0 {
			folders += ","
		}
		folders += fmt.Sprintf(`{"folderUri":%q}`, u)
	}
	lastActive := ""
	if len(urls) > 0 {
		lastActive = urls[0]
	}
	return fmt.Sprintf(`{
  "telemetry": {"machineId": "<machine-id>", "oauthToken": "<token>"},
  "backupWorkspaces": {"folders": [%s]},
  "windowsState": {"lastActiveWindow": {"folder": %q}}
}`, folders, lastActive)
}

func TestScan_NoIDEInstalled(t *testing.T) {
	home := t.TempDir()
	imp, err := NewImporterAt(home, "")
	require.NoError(t, err)

	found, warnings, err := imp.Scan()
	require.NoError(t, err)
	assert.Empty(t, found)
	assert.Empty(t, warnings)
}

func TestScan_MissingStorageFile(t *testing.T) {
	// IDE folder present but storage.json absent → silent skip.
	home := t.TempDir()
	root, err := configRoot("darwin", home)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, antigravitySpec.dirName, "User", "globalStorage"), 0755))

	imp, err := NewImporterAt(home, "")
	require.NoError(t, err)

	found, warnings, err := imp.Scan()
	require.NoError(t, err)
	assert.Empty(t, found)
	assert.Empty(t, warnings)
}

func TestScan_CorruptStorageFile(t *testing.T) {
	home := t.TempDir()
	writeStorage(t, home, "{not valid json")

	imp, err := NewImporterAt(home, "")
	require.NoError(t, err)

	found, warnings, err := imp.Scan()
	require.NoError(t, err)     // corrupt = warning, not error
	assert.Empty(t, found)      // nothing parsed
	require.Len(t, warnings, 1) // D16: warning with cause
	assert.Equal(t, "Antigravity", warnings[0].IDE)
	assert.NotEmpty(t, warnings[0].Cause)
}

func TestScan_HappyPath(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "projets", "my-repo")
	makeGitRepo(t, repo)
	writeStorage(t, home, storageBody(fileURL(repo)))

	imp, err := NewImporterAt(home, "")
	require.NoError(t, err)

	found, warnings, err := imp.Scan()
	require.NoError(t, err)
	assert.Empty(t, warnings)
	require.Len(t, found, 1)
	assert.Equal(t, filepath.Clean(repo), found[0].Path)
	assert.Equal(t, "Antigravity", found[0].IDE)
}

func TestScan_FileURIDecoded(t *testing.T) {
	// A repo path containing a space must survive file:// round-trip.
	home := t.TempDir()
	repo := filepath.Join(home, "with space", "repo")
	makeGitRepo(t, repo)
	writeStorage(t, home, storageBody(fileURL(repo)))

	imp, err := NewImporterAt(home, "")
	require.NoError(t, err)

	found, _, err := imp.Scan()
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, filepath.Clean(repo), found[0].Path)
}

func TestScan_FiltersNonGit(t *testing.T) {
	home := t.TempDir()
	// A plain directory that is NOT a git repo.
	nonRepo := filepath.Join(home, "not-a-repo")
	require.NoError(t, os.MkdirAll(nonRepo, 0755))
	writeStorage(t, home, storageBody(fileURL(nonRepo)))

	imp, err := NewImporterAt(home, "")
	require.NoError(t, err)

	found, _, err := imp.Scan()
	require.NoError(t, err)
	assert.Empty(t, found) // filtered out by git.IsGitRepo
}

func TestScan_Deduplicates(t *testing.T) {
	// storageBody puts the first URL in both folders and lastActiveWindow,
	// so the same repo appears twice in the JSON → one FoundRepo.
	home := t.TempDir()
	repo := filepath.Join(home, "projets", "dup")
	makeGitRepo(t, repo)
	writeStorage(t, home, storageBody(fileURL(repo)))

	imp, err := NewImporterAt(home, "")
	require.NoError(t, err)

	found, _, err := imp.Scan()
	require.NoError(t, err)
	require.Len(t, found, 1)
}

func TestNewImporterAt_InvalidIDE(t *testing.T) {
	_, err := NewImporterAt(t.TempDir(), "foo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vscode")
	assert.Contains(t, err.Error(), "cursor")
}

func TestScan_SpecificIDE(t *testing.T) {
	// Both Antigravity and Cursor storage present, but --ide cursor restricts
	// the scan to Cursor only.
	home := t.TempDir()
	cursorSpec := ideSpec{name: "Cursor", dirName: "Cursor", canon: "cursor"}
	cursorRepo := filepath.Join(home, "cursor-proj")
	makeGitRepo(t, cursorRepo)
	antigravRepo := filepath.Join(home, "antigrav-proj")
	makeGitRepo(t, antigravRepo)

	writeStorageFor(t, home, cursorSpec, storageBody(fileURL(cursorRepo)))
	writeStorageFor(t, home, antigravitySpec, storageBody(fileURL(antigravRepo)))

	imp, err := NewImporterAt(home, "cursor")
	require.NoError(t, err)

	found, _, err := imp.Scan()
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, filepath.Clean(cursorRepo), found[0].Path)
	assert.Equal(t, "Cursor", found[0].IDE)
}

func TestScan_MultiIDE(t *testing.T) {
	// Multiple IDE folders all scanned; a repo known to two IDEs is deduped
	// cross-IDE and attributed to the first IDE in knownIDEs order (Cursor
	// precedes Antigravity, so the shared repo is attributed to Cursor — D9).
	home := t.TempDir()
	cursorSpec := ideSpec{name: "Cursor", dirName: "Cursor", canon: "cursor"}
	sharedRepo := filepath.Join(home, "shared")
	makeGitRepo(t, sharedRepo)
	cursorRepo := filepath.Join(home, "cursor-only")
	makeGitRepo(t, cursorRepo)
	antigravRepo := filepath.Join(home, "antigrav-only")
	makeGitRepo(t, antigravRepo)

	writeStorageFor(t, home, cursorSpec,
		storageBody(fileURL(sharedRepo), fileURL(cursorRepo)))
	writeStorageFor(t, home, antigravitySpec,
		storageBody(fileURL(sharedRepo), fileURL(antigravRepo)))

	imp, err := NewImporterAt(home, "")
	require.NoError(t, err)

	found, _, err := imp.Scan()
	require.NoError(t, err)
	require.Len(t, found, 3, "shared + cursor-only + antigrav-only, deduped")

	byPath := make(map[string]string, len(found))
	for _, f := range found {
		byPath[f.Path] = f.IDE
	}
	assert.Equal(t, "Cursor", byPath[filepath.Clean(sharedRepo)],
		"shared repo attributed to first scanning IDE (Cursor)")
	assert.Equal(t, "Cursor", byPath[filepath.Clean(cursorRepo)])
	assert.Equal(t, "Antigravity", byPath[filepath.Clean(antigravRepo)])
}
