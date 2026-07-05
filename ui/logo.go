package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// boulezArt is the static block ASCII art for "BOULEZ", in the same ╗/╔/╝/╚/═/║
// style as the legacy cs2/squad fallback logo so it matches the TUI's existing
// aesthetic. It is the single source of truth for the logo glyph: callers only
// ever see LogoFrame, which colorizes it.
const boulezArt = `
__/\\\\\\\\\\\\\_________/\\\\\_______/\\\________/\\\__/\\\______________/\\\\\\\\\\\\\\\__/\\\\\\\\\\\\\\\_        
 _\/\\\/////////\\\_____/\\\///\\\____\/\\\_______\/\\\_\/\\\_____________\/\\\///////////__\////////////\\\__       
  _\/\\\_______\/\\\___/\\\/__\///\\\__\/\\\_______\/\\\_\/\\\_____________\/\\\_______________________/\\\/___      
   _\/\\\\\\\\\\\\\\___/\\\______\//\\\_\/\\\_______\/\\\_\/\\\_____________\/\\\\\\\\\\\_____________/\\\/_____     
    _\/\\\/////////\\\_\/\\\_______\/\\\_\/\\\_______\/\\\_\/\\\_____________\/\\\///////____________/\\\/_______    
     _\/\\\_______\/\\\_\//\\\______/\\\__\/\\\_______\/\\\_\/\\\_____________\/\\\_________________/\\\/_________   
      _\/\\\_______\/\\\__\///\\\__/\\\____\//\\\______/\\\__\/\\\_____________\/\\\_______________/\\\/___________  
       _\/\\\\\\\\\\\\\/_____\///\\\\\/______\///\\\\\\\\\/___\/\\\\\\\\\\\\\\\_\/\\\\\\\\\\\\\\\__/\\\\\\\\\\\\\\\_ 
        _\/////////////_________\/////__________\/////////_____\///////////////__\///////////////__\///////////////__
`

// logoPalette is the color gradient swept across the logo rows each frame.
// It reuses Boulez's on-brand agent-badge colors (pi purple #7D56F4 → aider
// teal #36CFC9) so the logo ties into the rest of the TUI's palette rather
// than introducing a new one. DRY: the brand colors live here as a gradient,
// not duplicated from programBadgeColors (which maps per-agent, not per-row).
var logoPalette = []lipgloss.Color{
	lipgloss.Color("#7D56F4"), // purple
	lipgloss.Color("#5B6BE8"),
	lipgloss.Color("#4285F4"), // blue
	lipgloss.Color("#2AA7C9"),
	lipgloss.Color("#36CFC9"), // teal
	lipgloss.Color("#2AA7C9"),
	lipgloss.Color("#4285F4"),
	lipgloss.Color("#5B6BE8"),
}

// LogoFrame renders the Boulez logo with a color gradient whose phase is
// offset by frame. When called repeatedly with an incrementing frame index
// (e.g. once per preview tick), the gradient appears to flow down through
// the rows — a cheap, dependency-free animation that reuses the existing
// 10 fps previewTickMsg cadence rather than spawning a new timer.
//
// Pure: no state, no I/O. The frame index is the only input, which makes it
// trivial to test (golden output at frame 0, frame N) and keeps the animation
// authority in the panes that own the counter.
func LogoFrame(frame int) string {
	lines := strings.Split(boulezArt, "\n")
	rendered := make([]string, len(lines))
	n := len(logoPalette)
	for i, line := range lines {
		// Phase-shift the gradient by the frame so colors flow downward over
		// time. Modulo on a non-zero n (logoPalette is package-constant).
		color := logoPalette[((i+frame)%n+n)%n]
		rendered[i] = lipgloss.NewStyle().Foreground(color).Render(strings.TrimRight(line, " "))
	}
	return lipgloss.JoinVertical(lipgloss.Center, rendered...)
}
