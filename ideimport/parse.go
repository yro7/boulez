// Package ideimport scans IDE state files for recently-opened folders and
// reports the ones that are git repositories, so they can be added to the cs2
// repo registry as a one-shot, manual import.
//
// The IDE state format (VS Code's storage.json and its forks) is undocumented
// and unstable. To survive format drift, the parser walks the JSON tree
// recursively and collects every file:// URL it finds, without assuming any
// key names. Only file:// URLs are collected: bare absolute paths and other
// string values (machine IDs, tokens, etc.) are deliberately ignored to
// avoid false positives and to never surface sensitive identifiers
// (AGENTS.md: no sensitive leaks).
//
// This package does NOT touch the cs2 registry. Importer.Scan returns
// discovered paths; the caller (the CLI) is responsible for Registry.Add.
package ideimport

import (
	"sort"
	"strings"
)

// collectPaths walks an arbitrary JSON tree (as produced by encoding/json's
// Unmarshal into any) and returns every string value that is a file:// URL.
//
// Deduplication within the tree and a sorted output make the result
// deterministic. Decoding the file:// URL into a filesystem path and filtering
// against git.IsGitRepo happen later in Importer.Scan — collectPaths only
// harvests candidate strings.
func collectPaths(node any) []string {
	seen := make(map[string]struct{})
	var walk func(n any)
	walk = func(n any) {
		switch v := n.(type) {
		case map[string]any:
			for _, val := range v {
				walk(val)
			}
		case []any:
			for _, val := range v {
				walk(val)
			}
		case string:
			if strings.HasPrefix(v, "file://") {
				seen[v] = struct{}{}
			}
		}
	}
	walk(node)
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
