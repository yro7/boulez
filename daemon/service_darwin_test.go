//go:build darwin

package daemon

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRenderPlist_ContainsRequiredKeys proves the generated launchd plist has
// the keys the acceptance criteria depend on: RunAtLoad + KeepAlive (survives
// reboot / crash), the stable Label, and ProgramArguments = `boulez daemon run`.
// This is the testable core of `boulez daemon install` on macOS — the launchctl
// bootstrap itself is an integration step we don't exercise here.
func TestRenderPlist_ContainsRequiredKeys(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderPlist(&buf, "/usr/local/bin/boulez",
		"/tmp/out.log", "/tmp/err.log"))

	xml := buf.String()
	assert.Contains(t, xml, "<key>Label</key>")
	assert.Contains(t, xml, "<string>"+ServiceLabel+"</string>")
	assert.Contains(t, xml, "<key>RunAtLoad</key>")
	assert.Contains(t, xml, "<true/>")
	assert.Contains(t, xml, "<key>KeepAlive</key>")
	assert.Contains(t, xml, "<true/>")
	// ProgramArguments = <exe> daemon run
	assert.Contains(t, xml, "/usr/local/bin/boulez")
	assert.Contains(t, xml, "<string>daemon</string>")
	assert.Contains(t, xml, "<string>run</string>")
	// Redirect logs so a crash is diagnosable.
	assert.Contains(t, xml, "/tmp/out.log")
	assert.Contains(t, xml, "/tmp/err.log")
	// Valid plist header.
	assert.True(t, strings.HasPrefix(xml, `<?xml version="1.0" encoding="UTF-8"?>`))
}
