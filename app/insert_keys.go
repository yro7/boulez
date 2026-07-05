package app

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/yro7/boulez/session"
)

// forwardInsertKey is the "pure injection" path for insert mode: it translates
// a bubbletea KeyMsg into the corresponding `tmux send-keys` invocation(s) on
// the instance and dispatches them immediately. The TUI holds NO text buffer
// of its own — each keystroke is forwarded as-is, so the agent's own
// readline/editor (and its backspace, history, completion, multi-line
// support, IME) is the authority. This is what makes insert mode behave like
// an attached terminal rather than a chat-style input that can only grow.
//
// Esc is NOT forwarded: it exits insert mode at the caller (handleInsertState),
// matching vim's contract. Ctrl+C IS forwarded (as C-c) so the user can
// interrupt a runaway agent without leaving insert mode — the global quit
// guard is scoped to state != stateInsert, so Ctrl+C reaches here.
//
// Unmapped keys (exotic combos tmux doesn't name) are dropped silently rather
// than surfacing an error mid-typing. The common set — printable runes,
// Enter, Backspace/Delete, Tab, arrows, Home/End, PageUp/Down, Insert,
// Ctrl+A..Z, Ctrl+\/]/^/_, Alt+letter, Alt+arrow, F1–F20 — is covered.
//
// This is currently the single call site for tmuxNamedKey. Per AGENTS.md (one
// adapter is a hypothetical seam, two make a real one) the translation stays
// here as a free function rather than a shared package; a second consumer
// would justify extracting it.
func forwardInsertKey(inst *session.Instance, msg tea.KeyMsg) error {
	if msg.Type == tea.KeyEsc {
		return nil // caller exits insert mode
	}
	// Named (non-literal) keys: arrows, editing keys, Ctrl+letter, function keys.
	if name, ok := tmuxNamedKey(msg); ok {
		if msg.Alt {
			name = "M-" + name
		}
		return inst.SendKey(name)
	}
	// Printable runes: literal, via send-keys -l (one tmux call for all runes).
	if msg.Type == tea.KeyRunes {
		if msg.Alt {
			// Alt+letter is meta in tmux (M-x); send M-<rune> per rune.
			// Multi-rune Alt events are rare (some IMEs) but handled correctly.
			for _, r := range msg.Runes {
				if err := inst.SendKey("M-" + string(r)); err != nil {
					return err
				}
			}
			return nil
		}
		return inst.SendKeys(string(msg.Runes))
	}
	return nil // unmapped key: drop silently
}

// tmuxNamedKey returns the tmux key name for a named (non-rune) KeyMsg, or
// ok=false if the key has no standard tmux name. The names are tmux's key
// parser literals (see tmux(1) man page, "KEY NAMES"): BSpace, Delete, Tab,
// BTab (back-tab = Shift+Tab), Up/Down/Left/Right, Home/End, PageUp/PageDown,
// Insert, Space, and the C-<letter> / M-<key> / F<n> forms. The Alt modifier
// is applied by the caller (as an "M-" prefix) so this function reports the
// base key only.
func tmuxNamedKey(msg tea.KeyMsg) (string, bool) {
	switch msg.Type {
	case tea.KeyEnter:
		return "Enter", true
	case tea.KeyBackspace:
		return "BSpace", true
	case tea.KeyDelete:
		return "Delete", true
	case tea.KeyTab:
		return "Tab", true
	case tea.KeyShiftTab:
		return "BTab", true
	case tea.KeyUp:
		return "Up", true
	case tea.KeyDown:
		return "Down", true
	case tea.KeyLeft:
		return "Left", true
	case tea.KeyRight:
		return "Right", true
	case tea.KeyHome:
		return "Home", true
	case tea.KeyEnd:
		return "End", true
	case tea.KeyPgUp:
		return "PageUp", true
	case tea.KeyPgDown:
		return "PageDown", true
	case tea.KeyInsert:
		return "Insert", true
	case tea.KeySpace:
		return "Space", true
	case tea.KeyCtrlBackslash:
		return `C-\`, true
	case tea.KeyCtrlCloseBracket:
		return "C-]", true
	case tea.KeyCtrlCaret:
		return "C-^", true
	case tea.KeyCtrlUnderscore:
		return "C-_", true
	}
	// Ctrl+A..Ctrl+Z (types 1..26). Note KeyCtrlM == KeyEnter (both keyCR=13);
	// bubbletea collapses them, so Enter is matched by the KeyEnter case above
	// and this branch only fires for the distinct Ctrl+letter keys.
	if msg.Type >= tea.KeyCtrlA && msg.Type <= tea.KeyCtrlZ {
		return "C-" + string(rune('a'+(msg.Type-tea.KeyCtrlA))), true
	}
	// Function keys F1..F20.
	if msg.Type >= tea.KeyF1 && msg.Type <= tea.KeyF20 {
		return fmt.Sprintf("F%d", int(msg.Type-tea.KeyF1+1)), true
	}
	return "", false
}
