package keys

import (
	"github.com/charmbracelet/bubbles/key"
)

type KeyName int

const (
	KeyUp KeyName = iota
	KeyDown
	KeyEnter
	KeyNew
	KeyKill
	KeyQuit
	KeyReview
	KeyPush
	KeySubmit

	// KeyLand lands the instance's branch into the trunk (main by default):
	// commit+push then merge into main. Top-level explicit action, behind a
	// confirmation modal. Uppercase to match the destructive-action style
	// (D=kill) and avoid colliding with lowercase bindings.
	KeyLand

	KeyTab        // Tab is a special keybinding for switching between panes.
	KeySubmitName // SubmitName is a special keybinding for submitting the name of a new instance.

	KeyCheckout
	KeyResume
	KeyPrompt // New key for entering a prompt
	KeyHelp   // Key for showing help screen

	// KeyQuickSession opens the named-preset picker (Ctrl+R). A preset is a
	// complete recipe (host+repo+profile+prompt+branch) that skips the
	// host/repo/prompt selectors, leaving only the instance name to type.
	KeyQuickSession

	// Diff keybindings
	KeyShiftUp
	KeyShiftDown

	// Reorder keybindings
	KeyMoveUp
	KeyMoveDown

	// KeyToggleAutoYes toggles the per-instance AutoYes flag.
	KeyToggleAutoYes

	// KeyInsert enters insert mode in the preview pane: keystrokes are
	// forwarded directly to the selected instance's tmux pane (pure injection
	// via Instance.SendKey/SendKeys) instead of being interpreted as fleet
	// keybindings. Exited with Esc. Vim-style modal separation so fleet
	// bindings (q, c, r, ...) never collide with text the user types into the
	// agent. Backspace, arrows, history, Ctrl-C, etc. all work because the
	// agent's own readline is the authority — the TUI holds no text buffer.
	KeyInsert
	// KeySpawnOrchestrator spawns a new orchestrator instance (Shift+O). The
	// orchestrator is an ordinary fleet instance (KindOrchestrator, headless
	// worktree) that supervises the fleet via `boulez ctl`. This is the manual,
	// on-demand replacement for the old always-on "instance 0" bootstrap: the
	// user spawns one when they want one, nothing is auto-spawned at startup.
	KeySpawnOrchestrator

	// KeyRestore opens the archived-instances picker (U for Undo delete):
	// lists soft-deleted instances still within their retention window so the
	// user can restore one. Restoring recreates the tmux session and returns
	// the instance to Ready.
	KeyRestore
)

// GlobalKeyStringsMap is a global, immutable map string to keybinding.
var GlobalKeyStringsMap = map[string]KeyName{
	"up":         KeyUp,
	"k":          KeyUp,
	"down":       KeyDown,
	"j":          KeyDown,
	"shift+up":   KeyShiftUp,
	"shift+down": KeyShiftDown,
	"J":          KeyMoveDown,
	"K":          KeyMoveUp,
	"a":          KeyToggleAutoYes,
	"N":          KeyPrompt,
	"R":          KeyQuickSession,
	"O":          KeySpawnOrchestrator,
	"U":          KeyRestore,
	"i":          KeyInsert,
	"enter":      KeyEnter,
	"o":          KeyEnter,
	"n":          KeyNew,
	"D":          KeyKill,
	"q":          KeyQuit,
	"tab":        KeyTab,
	"c":          KeyCheckout,
	"r":          KeyResume,
	"p":          KeySubmit,
	"L":          KeyLand,
	"?":          KeyHelp,
}

// GlobalkeyBindings is a global, immutable map of KeyName tot keybinding.
var GlobalkeyBindings = map[KeyName]key.Binding{
	KeyUp: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	),
	KeyDown: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	),
	KeyShiftUp: key.NewBinding(
		key.WithKeys("shift+up"),
		key.WithHelp("shift+↑", "scroll"),
	),
	KeyShiftDown: key.NewBinding(
		key.WithKeys("shift+down"),
		key.WithHelp("shift+↓", "scroll"),
	),
	KeyEnter: key.NewBinding(
		key.WithKeys("enter", "o"),
		key.WithHelp("↵/o", "open"),
	),
	KeyNew: key.NewBinding(
		key.WithKeys("n"),
		key.WithHelp("n", "new"),
	),
	KeyKill: key.NewBinding(
		key.WithKeys("D"),
		key.WithHelp("D", "kill"),
	),
	KeyHelp: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
	KeyQuit: key.NewBinding(
		key.WithKeys("q"),
		key.WithHelp("q", "quit"),
	),
	KeySubmit: key.NewBinding(
		key.WithKeys("p"),
		key.WithHelp("p", "push branch"),
	),
	KeyLand: key.NewBinding(
		key.WithKeys("L"),
		key.WithHelp("L", "land → main"),
	),
	KeyPrompt: key.NewBinding(
		key.WithKeys("N"),
		key.WithHelp("N", "new with prompt"),
	),
	KeyQuickSession: key.NewBinding(
		key.WithKeys("R"),
		key.WithHelp("R", "quick session"),
	),
	KeyCheckout: key.NewBinding(
		key.WithKeys("c"),
		key.WithHelp("c", "checkout"),
	),
	KeyTab: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch tab"),
	),
	KeyResume: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "resume"),
	),

	KeyMoveUp: key.NewBinding(
		key.WithKeys("K"),
		key.WithHelp("K", "move up"),
	),
	KeyMoveDown: key.NewBinding(
		key.WithKeys("J"),
		key.WithHelp("J", "move down"),
	),

	KeyToggleAutoYes: key.NewBinding(
		key.WithKeys("a"),
		key.WithHelp("a", "toggle auto-yes"),
	),

	KeySpawnOrchestrator: key.NewBinding(
		key.WithKeys("O"),
		key.WithHelp("O", "spawn orchestrator"),
	),
	KeyRestore: key.NewBinding(
		key.WithKeys("U"),
		key.WithHelp("U", "restore archived"),
	),
	KeyInsert: key.NewBinding(
		key.WithKeys("i"),
		key.WithHelp("i", "insert mode"),
	),

	// -- Special keybindings --

	KeySubmitName: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "submit name"),
	),
}
