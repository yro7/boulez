package program

import "strings"

// GeminiAdapter carries all agent-specific knowledge for Gemini CLI.
// Strings ported verbatim from session/tmux/tmux.go (pre-refactor).
type GeminiAdapter struct{}

func (GeminiAdapter) Name() string { return "gemini" }

func (GeminiAdapter) Matches(program string) bool {
	// Pre-refactor: strings.HasPrefix(t.program, ProgramGemini) where
	// ProgramGemini == "gemini".
	return strings.HasPrefix(program, "gemini")
}

func (GeminiAdapter) Detect(content string) (Status, *Prompt) {
	// Ready: gemini's "Yes, allow once" permission prompt.
	if strings.Contains(content, "Yes, allow once") {
		return StatusReady, &Prompt{Kind: PromptReady}
	}
	return StatusWorking, nil
}
