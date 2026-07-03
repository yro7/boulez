package program

import "strings"

// ClaudeAdapter carries all agent-specific knowledge for Claude Code.
// Strings ported verbatim from session/tmux/tmux.go (pre-refactor) so that
// behaviour is unchanged.
type ClaudeAdapter struct{}

func (ClaudeAdapter) Name() string { return "claude" }

func (ClaudeAdapter) Matches(program string) bool {
	// Pre-refactor: strings.HasSuffix(t.program, ProgramClaude) where
	// ProgramClaude == "claude".
	return strings.HasSuffix(program, "claude")
}

func (ClaudeAdapter) Detect(content string) (Status, *Prompt) {
	// Trust / MCP prompts -> permission, resolved by tapping Enter.
	if strings.Contains(content, "Do you trust the files in this folder?") ||
		strings.Contains(content, "new MCP server") {
		return StatusPermission, &Prompt{
			Kind:    PromptTrust,
			Message: "trust/MCP prompt",
			Resolve: func(r Responder) error { return r.TapEnter() },
		}
	}
	// Ready: agent waiting for free user input.
	if strings.Contains(content, "No, and tell Claude what to do differently") {
		return StatusReady, &Prompt{Kind: PromptReady}
	}
	return StatusWorking, nil
}
