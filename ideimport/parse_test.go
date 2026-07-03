package ideimport

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCollectPaths_Recursive walks a representative (sanitized) VS Code-family
// storage.json tree and asserts that only file:// URLs are collected — from
// any nesting depth — while machine IDs, tokens, numbers, booleans, null and
// bare absolute paths are ignored (D7: file:// only, no sensitive leaks).
func TestCollectPaths_Recursive(t *testing.T) {
	raw := `{
		"telemetry": {"machineId": "<machine-id>", "oauthToken": "<token>"},
		"backupWorkspaces": {
			"folders": [
				{"folderUri": "file:///Users/user/projets/repo-a"},
				{"folderUri": "file:///Users/user/projets/repo-b"}
			],
			"workspaces": [
				{"configuration": {"folders": [{"uri": "file:///Users/user/projets/repo-c"}]}}
			]
		},
		"windowsState": {
			"lastActiveWindow": {"folder": "file:///Users/user/projets/repo-a"},
			"openedWindows": [
				{"folder": "file:///Users/user/projets/repo-d"},
				{"configUri": "file:///Users/user/projets/mono.code-workspace"}
			]
		},
		"count": 42,
		"flag": true,
		"nothing": null,
		"bareAbsolute": "/Users/user/projets/repo-e"
	}`
	var node any
	require.NoError(t, json.Unmarshal([]byte(raw), &node))

	got := collectPaths(node)

	// Sorted + deduped: repo-a appears twice (folders + lastActiveWindow) but
	// yields a single entry. Sensitive and bare-path values are absent.
	want := []string{
		"file:///Users/user/projets/mono.code-workspace",
		"file:///Users/user/projets/repo-a",
		"file:///Users/user/projets/repo-b",
		"file:///Users/user/projets/repo-c",
		"file:///Users/user/projets/repo-d",
	}
	assert.Equal(t, want, got)
}

// TestCollectPaths_NonTreeValues ensures scalar leaves and nil produce no
// paths and never panic.
func TestCollectPaths_NonTreeValues(t *testing.T) {
	for _, node := range []any{nil, 42, 3.14, true, "plain string", ""} {
		assert.Empty(t, collectPaths(node), "node=%v", node)
	}
	assert.Empty(t, collectPaths(map[string]any{}))
	assert.Empty(t, collectPaths([]any{}))
}
