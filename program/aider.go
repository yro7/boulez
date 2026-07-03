package program

import "strings"

// AiderAdapter carries all agent-specific knowledge for Aider.
// Strings ported verbatim from session/tmux/tmux.go (pre-refactor).
type AiderAdapter struct{}

func (AiderAdapter) Name() string { return "aider" }

func (AiderAdapter) Matches(program string) bool {
	// Pre-refactor: strings.HasPrefix(t.program, ProgramAider) where
	// ProgramAider == "aider".
	return strings.HasPrefix(program, "aider")
}

func (AiderAdapter) Detect(content string) (Status, *Prompt) {
	// Ready: aider's (Y)es/(N)o/(D)on't ask again prompt.
	if strings.Contains(content, "(Y)es/(N)o/(D)on't ask again") {
		return StatusReady, &Prompt{Kind: PromptReady}
	}
	return StatusWorking, nil
}
