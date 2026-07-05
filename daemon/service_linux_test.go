//go:build linux

package daemon

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRenderUnit_ContainsRequiredKeys proves the generated systemd user unit
// has the keys acceptance depends on: Restart=on-failure (survives crash),
// WantedBy=default.target (starts at login), and ExecStart = `boulez daemon run`.
func TestRenderUnit_ContainsRequiredKeys(t *testing.T) {
	content := renderUnit("/usr/local/bin/boulez", "/tmp/out.log", "/tmp/err.log")

	assert.Contains(t, content, "ExecStart=/usr/local/bin/boulez daemon run")
	assert.Contains(t, content, "Restart=on-failure")
	assert.Contains(t, content, "WantedBy=default.target")
	assert.Contains(t, content, "StandardOutput=append:/tmp/out.log")
	assert.Contains(t, content, "StandardError=append:/tmp/err.log")
	// Reasonable unit structure.
	require.True(t, strings.Contains(content, "[Unit]"))
	require.True(t, strings.Contains(content, "[Service]"))
	require.True(t, strings.Contains(content, "[Install]"))
}
