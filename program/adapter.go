// Package program is the seam between the agent-agnostic core (tmux session
// management) and the agent-specific knowledge (how to detect that an agent
// is waiting for input, asking for a permission, or running).
//
// Adding support for a new agent (e.g. Pi) is done by creating a single file
// in this package implementing Adapter and registering it in init() in
// program.go. The tmux/session/app/daemon packages never need editing.
package program

// Responder is the minimal surface an Adapter needs to act on the underlying
// tmux session (send keystrokes). Deliberately narrow so adapters do not
// depend on *TmuxSession and stay unit-testable without a PTY or tmux.
type Responder interface {
	TapEnter() error     // send 0x0D
	TapDAndEnter() error // send 'D' + 0x0D
	SendKeys(keys string) error
}

// Status is the perceived state of the agent derived from the tmux pane
// content.
type Status int

const (
	StatusUnknown Status = iota
	// StatusWorking: the agent is producing / running, not waiting on the user.
	StatusWorking
	// StatusReady: the agent is waiting for free user input (the "ready" badge
	// in the TUI). Not a permission request.
	StatusReady
	// StatusPermission: the agent is asking for a permission/approval/trust
	// decision that the auto-yes daemon may resolve automatically.
	StatusPermission
)

// PromptKind classifies a detected prompt.
type PromptKind int

const (
	PromptNone PromptKind = iota
	// PromptTrust: a first-run trust prompt ("do you trust the files in this
	// folder?") or an MCP server approval.
	PromptTrust
	// PromptPermission: a per-action permission request ("allow this command?").
	PromptPermission
	// PromptReady: agent is idle waiting for the user to type something.
	PromptReady
)

// Prompt describes a prompt detected in the pane, with an optional resolution
// action (auto-yes). Decouples "detect" from "act".
type Prompt struct {
	Kind    PromptKind
	Message string // human-readable extract, for future TUI display
	// Resolve, if non-nil, is invoked by the auto-yes daemon / trust handler to
	// dismiss the prompt automatically. Nil means "nothing to do automatically".
	Resolve func(Responder) error
}

// Adapter carries ALL agent-specific knowledge. One adapter per agent.
// Adding Pi = create program/pi.go and register it. Nothing else changes.
type Adapter interface {
	// Name returns the canonical identifier (e.g. "pi", "claude", "aider").
	Name() string

	// Matches reports whether this adapter handles the given `program` command
	// (e.g. "/path/to/claude" -> claude adapter). First match in registry
	// order wins.
	Matches(program string) bool

	// Detect inspects the tmux pane content and returns the perceived status
	// plus an optional Prompt to resolve. A pure function of `content` ->
	// testable without tmux, without a PTY, without anything.
	Detect(content string) (Status, *Prompt)
}

// NoOpAdapter is the default fallback returned by Lookup when no registered
// adapter matches. An unknown agent never crashes: it is simply "silent"
// (StatusUnknown, no prompt), so it gets no "Ready" badge and no auto-yes.
type NoOpAdapter struct{}

func (NoOpAdapter) Name() string            { return "noop" }
func (NoOpAdapter) Matches(string) bool     { return false } // never matches by default
func (NoOpAdapter) Detect(string) (Status, *Prompt) {
	return StatusUnknown, nil
}
