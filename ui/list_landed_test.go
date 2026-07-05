package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yro7/boulez/session"
)

// newRenderer builds an InstanceRenderer wide enough that truncation does not
// hide the title or icon under test.
func newRenderer() *InstanceRenderer {
	s := spinner.New()
	r := &InstanceRenderer{spinner: &s}
	r.setWidth(80)
	return r
}

func newRenderInstance(t *testing.T, title string) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		Title:   title,
		Path:    ".",
		Program: "echo",
	})
	require.NoError(t, err)
	return inst
}

// TestRender_LandedShowsCheckmark proves a landed instance renders the green
// checkmark icon instead of the status dot, so the user sees at a glance that
// the branch has been merged.
func TestRender_LandedShowsCheckmark(t *testing.T) {
	inst := newRenderInstance(t, "feat-x")
	inst.SetStatus(session.Ready)
	inst.SetLanded(true)

	out := newRenderer().Render(inst, 0, false, false)
	assert.True(t, strings.Contains(out, "✓"), "landed shows checkmark: %q", out)
	assert.False(t, strings.Contains(out, "●"), "landed hides the ready dot: %q", out)
}

// TestRender_LandingShowsSpinner proves a land in flight shows the spinner,
// taking priority over the landed checkmark (so the spinner is visible even on
// an already-landed re-land).
func TestRender_LandingShowsSpinner(t *testing.T) {
	inst := newRenderInstance(t, "feat-x")
	inst.SetStatus(session.Ready)
	inst.SetLanded(true)
	inst.SetLanding(true)

	out := newRenderer().Render(inst, 0, false, false)
	assert.False(t, strings.Contains(out, "✓"), "landing hides the checkmark (spinner wins): %q", out)
}

// TestRender_NotLandedShowsStatusIcon proves a non-landed Ready instance keeps
// the ready dot — the landed hint does not bleed into normal rendering.
func TestRender_NotLandedShowsStatusIcon(t *testing.T) {
	inst := newRenderInstance(t, "feat-x")
	inst.SetStatus(session.Ready)

	out := newRenderer().Render(inst, 0, false, false)
	assert.True(t, strings.Contains(out, "●"), "ready dot shown when not landed: %q", out)
	assert.False(t, strings.Contains(out, "✓"), "checkmark hidden when not landed: %q", out)
}
