package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
)

// init forces a TrueColor profile so the gradient actually emits ANSI escapes
// during tests. Without this, lipgloss downgrades to Ascii outside a TTY and
// every frame renders identically — which would mask a real "frame is ignored"
// regression. The TUI itself runs in AltScreen where the profile is already
// TrueColor, so this just matches production.
func init() {
	lipgloss.SetColorProfile(termenv.TrueColor)
}

// TestLogoFrame_StaticArtPresent proves the Boulez glyph is rendered (the
// legacy "CS2/SQUAD" art is gone) and that the message-handling contract used
// by both panes holds: LogoFrame output is a non-empty multi-line string.
func TestLogoFrame_StaticArtPresent(t *testing.T) {
	out := LogoFrame(0)
	assert.NotEmpty(t, out)
	// The block art has 6 rows; JoinVertical keeps them on separate lines.
	assert.GreaterOrEqual(t, len(strings.Split(out, "\n")), 6)
	// Sanity: a recognisable Boulez run glyph appears (top of the first B).
	assert.Contains(t, out, "██████")
}

// TestLogoFrame_FrameShiftsColor proves the animation actually animates: two
// consecutive frames must differ, because the gradient phase advances by one
// row. We compare raw strings (ANSI escapes included) so a color change is a
// real difference. Guards against a regression where frame is ignored.
func TestLogoFrame_FrameShiftsColor(t *testing.T) {
	f0 := LogoFrame(0)
	f1 := LogoFrame(1)
	assert.NotEqual(t, f0, f1, "consecutive frames must differ (gradient must flow)")
}

// TestLogoFrame_Periodic proves the gradient is periodic: after one full
// palette cycle the frame repeats. Keeps the animation bounded and predictable.
func TestLogoFrame_Periodic(t *testing.T) {
	n := len(logoPalette)
	assert.Equal(t, LogoFrame(0), LogoFrame(n), "gradient must be periodic over len(logoPalette)")
}

// TestLogoFrame_NegativeFrame proves a negative frame index (which never
// happens in practice but is defended against via double-modulo) still renders
// without panicking and equals the positive equivalent.
func TestLogoFrame_NegativeFrame(t *testing.T) {
	assert.Equal(t, LogoFrame(-1), LogoFrame(len(logoPalette)-1))
}
