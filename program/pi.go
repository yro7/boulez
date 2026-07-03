package program

import "strings"

// PiAdapter carries all agent-specific knowledge for the Pi coding agent
// (https://pi.dev). Pi runs in a tmux pane like the other agents.
//
// STATUS DETECTION (current):
// Pi's footer line is stable across models/thinking levels and identifies
// the agent unambiguously: it ends with "<provider> <model> • <thinking-level>"
// (e.g. "<provider> <model> • high") and contains a context-usage percentage
// like "0.4%/1.0M". We use this signature to confirm Pi is the running agent.
//
// We currently return StatusWorking whenever Pi is detected. Distinguishing
// StatusReady (idle, waiting for input) from StatusWorking reliably requires
// a stable indicator of the "thinking" state; the animated working spinner
// does not survive tmux capture-pane reliably, so ready/permission detection
// is intentionally left as a no-op until a stable signature is confirmed.
//
// This is deliberately conservative: a wrong "Ready" badge is worse than no
// badge. Pi still gets full lifecycle (worktree, attach, diff, preview) via
// the agent-agnostic core; only auto-yes and the Ready badge are silent.
type PiAdapter struct{}

// PiReadySentinel is the marker string the pi-cs2 Pi extension appends to the
// end of every completed assistant turn. CS2 detects this string in the tmux
// pane content to know Pi is idle and waiting for input (StatusReady).
//
// This is a shared contract between two codebases:
//   - emitter: the pi-cs2 extension (~/cs-multirepo/extensions/pi-cs2.ts) prints
//     this exact string via sendMessage with display:true after each turn.
//   - detector: program.PiAdapter.Detect looks for it in the captured pane.
//
// Keep them in sync. The string is deliberately distinctive and unlikely to
// appear in normal output.
const PiReadySentinel = "⟦cs2:ready⟧"

func (PiAdapter) Name() string { return "pi" }

func (PiAdapter) Matches(program string) bool {
	// Match the bare command "pi" regardless of path, but not "ping" etc.
	base := program
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	// Strip a leading "exec " or similar just in case.
	base = strings.TrimSpace(base)
	return base == "pi"
}

// piFooterMarker is a stable substring of Pi's footer line. The footer always
// shows the thinking level after " • " (e.g. " • high", " • off", " • medium").
// Combined with the context-usage "%" this is a reliable Pi signature.
func (PiAdapter) Detect(content string) (Status, *Prompt) {
	if !isPiFooter(content) {
		return StatusUnknown, nil
	}
	// First, check for the CS2 sentinel emitted by the pi-cs2 extension (see
	// pi-cs2.ts). This is the most reliable ready signal: Pi prints it at the
	// end of each completed assistant turn, so its presence means Pi is idle
	// and waiting for input.
	if strings.Contains(content, PiReadySentinel) {
		return StatusReady, &Prompt{Kind: PromptReady}
	}
	// TODO: until the extension is installed, the sentinel won't appear. Once a
	// stable ready/working signature is identified, return
	// StatusReady here so the TUI shows the "Ready" badge and the daemon can
	// react. Until then, stay silent (StatusWorking) to avoid false badges.
	return StatusWorking, nil
}

// isPiFooter reports whether content shows a Pi footer line. We look for a
// line containing both a context-usage percentage and the " • " thinking-level
// separator that Pi always emits.
func isPiFooter(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "%/") && strings.Contains(line, " • ") {
			return true
		}
	}
	return false
}
