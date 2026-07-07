package program

import (
	"encoding/json"
	"strings"
)

// PiAdapter carries all agent-specific knowledge for the Pi coding agent
// (https://pi.dev). Pi runs in a tmux pane like the other agents, but its
// status is NOT read from pane content. Instead, Pi writes its session as an
// incrementally-appended JSONL file (appendFileSync on every message_end),
// and Boulez tails that file to detect the agent's state.
//
// STATUS DETECTION (journal-based):
// Each assistant message entry in the JSONL carries a stopReason field:
//   - "stop"     → Pi finished its turn and is waiting for input (StatusReady)
//   - "toolUse"  → Pi is mid-loop, executing tools (StatusWorking)
//   - "aborted"  → the turn was aborted (StatusUnknown — no auto-action)
//   - "error"    → the turn errored (StatusUnknown)
//   - "length"   → the response hit a length limit (StatusWorking — likely mid-turn)
// A trailing toolResult/user entry means the LLM will resume shortly
// (StatusWorking). This is deterministic: stopReason:"stop" is written only
// when Pi truly finishes, so no stability heuristic or sentinel is needed.
//
// The journal content (the last JSONL line) is passed to Detect as `content`.
// This replaces the former sentinel-based approach (pi-boulez.ts extension +
// ⟦boulez:ready⟧ sentinel + footer scraping), which was fragile: the animated
// spinner broke the stability heuristic, the sentinel required a shared
// contract across two codebases, and the footer regex was model-dependent.
type PiAdapter struct{}

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

// piSessionEntry is the JSONL line shape for a Pi message entry. Only the
// fields needed for status detection are decoded.
type piSessionEntry struct {
	Type    string `json:"type"`
	Message struct {
		Role       string `json:"role"`
		StopReason string `json:"stopReason"`
	} `json:"message"`
}

// Detect parses the last JSONL line (a Pi session journal entry) and returns
// the perceived status. A pure function of `content` → testable without a
// journal, without a PTY, without tmux. If the content is not valid JSON or
// not a message entry, returns StatusUnknown (we cannot determine the state).
func (PiAdapter) Detect(content string) (Status, *Prompt) {
	var entry piSessionEntry
	if err := json.Unmarshal([]byte(content), &entry); err != nil {
		return StatusUnknown, nil
	}
	if entry.Type != "message" {
		// Non-message entries (session, model_change, thinking_level_change)
		// appear between turns. We cannot infer status from them alone.
		return StatusUnknown, nil
	}

	switch entry.Message.Role {
	case "assistant":
		switch entry.Message.StopReason {
		case "stop":
			return StatusReady, &Prompt{Kind: PromptReady}
		case "toolUse", "length":
			return StatusWorking, nil
		default:
			// "error", "aborted", or empty — cannot act automatically.
			return StatusUnknown, nil
		}
	case "toolResult", "user":
		// A tool just finished or a prompt was just sent — the LLM will
		// resume shortly.
		return StatusWorking, nil
	default:
		return StatusUnknown, nil
	}
}

// SessionArgs implements JournalingAdapter. Pi supports --session-dir natively
// (CLI flag, settings, or PI_CODING_AGENT_SESSION_DIR env). We pass it
// explicitly so the journal observer and Pi agree on the path deterministically.
// The observer and Pi both point at the same directory; Pi creates a single
// .jsonl file inside it.
func (PiAdapter) SessionArgs(sessionDir string) []string {
	return []string{"--session-dir", sessionDir}
}
