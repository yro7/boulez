package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteControlFiles_WritesBothFiles proves the spawn-path control-dir setup
// writes both ORCHESTRATOR.md and .mcp.json with the right content, idempotently.
// It uses a temp config dir so it does not touch the user's ~/.boulez.
func TestWriteControlFiles_WritesBothFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp) // config.GetConfigDir uses $HOME under ~/.boulez

	id := "orch-123"
	if err := WriteControlFiles(id); err != nil {
		t.Fatalf("WriteControlFiles: %v", err)
	}

	dir, _ := ControlDir(id)

	// ORCHESTRATOR.md exists and carries the role + id.
	ctx, err := os.ReadFile(filepath.Join(dir, ContextFileName))
	if err != nil {
		t.Fatalf("read ORCHESTRATOR.md: %v", err)
	}
	if !strings.Contains(string(ctx), id) {
		t.Fatalf("ORCHESTRATOR.md missing instance id %q", id)
	}
	// The CLI doc must be gone (surface lives in MCP schemas now).
	if strings.Contains(string(ctx), "boulez ctl") {
		t.Fatalf("ORCHESTRATOR.md must not reference the CLI surface anymore:\n%s", ctx)
	}
	if !strings.Contains(string(ctx), "MCP server") {
		t.Fatalf("ORCHESTRATOR.md should mention the MCP server:\n%s", ctx)
	}

	// .mcp.json exists, is valid JSON, and points at `boulez mcp serve --as <id>`.
	mcpBytes, err := os.ReadFile(filepath.Join(dir, MCPConfigFileName))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	var cfg struct {
		McpServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(mcpBytes, &cfg); err != nil {
		t.Fatalf("unmarshal .mcp.json: %v\nraw: %s", err, mcpBytes)
	}
	srv, ok := cfg.McpServers["boulez"]
	if !ok {
		t.Fatalf(".mcp.json missing 'boulez' server: %s", mcpBytes)
	}
	if srv.Command != "boulez" {
		t.Fatalf("command = %q, want boulez", srv.Command)
	}
	wantArgs := []string{"mcp", "serve", "--as", id}
	if len(srv.Args) != len(wantArgs) {
		t.Fatalf("args = %v, want %v", srv.Args, wantArgs)
	}
	for i, a := range wantArgs {
		if srv.Args[i] != a {
			t.Fatalf("args[%d] = %q, want %q (args=%v)", i, srv.Args[i], a, srv.Args)
		}
	}
}

// TestWriteControlFiles_Idempotent proves a second write produces identical
// content (same id), so rewriting on every boulez restart is safe.
func TestWriteControlFiles_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	id := "orch-456"
	if err := WriteControlFiles(id); err != nil {
		t.Fatalf("first write: %v", err)
	}
	dir, _ := ControlDir(id)
	ctx1, _ := os.ReadFile(filepath.Join(dir, ContextFileName))
	mcp1, _ := os.ReadFile(filepath.Join(dir, MCPConfigFileName))

	if err := WriteControlFiles(id); err != nil {
		t.Fatalf("second write: %v", err)
	}
	ctx2, _ := os.ReadFile(filepath.Join(dir, ContextFileName))
	mcp2, _ := os.ReadFile(filepath.Join(dir, MCPConfigFileName))

	if string(ctx1) != string(ctx2) {
		t.Fatalf("ORCHESTRATOR.md changed on rewrite")
	}
	if string(mcp1) != string(mcp2) {
		t.Fatalf(".mcp.json changed on rewrite")
	}
}

// TestMCPConfigContent_QuotesID proves the id is JSON-quoted (handles special
// chars) rather than naively concatenated, so a hostile id cannot break the
// .mcp.json structure.
func TestMCPConfigContent_QuotesID(t *testing.T) {
	raw := MCPConfigContent(`a"b`)
	if !json.Valid([]byte(raw)) {
		t.Fatalf("MCPConfigContent produced invalid JSON for a quoted id:\n%s", raw)
	}
}
