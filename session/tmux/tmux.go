package tmux

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/yro7/boulez/cmd"
	"github.com/yro7/boulez/log"
	"github.com/yro7/boulez/program"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// TmuxSession represents a managed tmux session. It owns the tmux LIFECYCLE
// only (Start / has-session / kill-session / capture-pane / send-keys / status
// monitoring) — all via the SSH-aware cmdExec, no PTY. Interactive attach is
// NOT here: the TUI runs host.AttachCmd via tea.ExecProcess, which releases
// the Bubbletea terminal for the command's duration. The previous design kept
// a local creack/pty + io.Copy + stdin scavenger here, which froze the TUI
// over SSH (two readers on os.Stdin, no terminal release) — that machinery is
// gone. Restore exists only to (re)initialize the status monitor when a
// pre-existing session is reused.
type TmuxSession struct {
	// The name of the tmux session and the sanitized name used for tmux commands.
	sanitizedName string
	program       string
	// adapter carries the agent-specific detection logic for t.program.
	// Resolved once at construction via program.Lookup; tmux.go itself holds no
	// agent-specific strings. Adding a new agent never touches this file.
	adapter program.Adapter
	// cmdExec is used to execute commands in the tmux session.
	cmdExec cmd.Executor

	// monitor watches the tmux pane content hash to derive stableFor (the
	// idle-since signal). Initialized by Start or Restore.
	monitor *statusMonitor
}

const TmuxPrefix = "boulez_"

// Compile-time guarantee that *TmuxSession satisfies program.Responder, so the
// adapter's Resolve callbacks (TapEnter/TapDAndEnter/SendKeys) can act on a
// session. If this ever breaks, the program.Adapter seam is broken.
var _ program.Responder = (*TmuxSession)(nil)

var whiteSpaceRegex = regexp.MustCompile(`\s+`)

func toBoulezTmuxName(str string) string {
	// PII: str is the instance Title only — the host alias is never passed in
	// — so a remote host never appears in tmux session names (decision 5).
	// The host lives only in InstanceData.Host (local bookkeeping).
	str = whiteSpaceRegex.ReplaceAllString(str, "")
	str = strings.ReplaceAll(str, ".", "_") // tmux replaces all . with _
	return fmt.Sprintf("%s%s", TmuxPrefix, str)
}

// SessionName returns the deterministic tmux session name for an instance
// with the given title. It is the single source of truth for the mapping
// title → session name, so other packages (e.g. the daemon's orchestrator
// bootstrap, which must reason about a session by name without constructing
// a TmuxSession) cannot drift from the sanitization logic above.
func SessionName(title string) string {
	return toBoulezTmuxName(title)
}

// SessionExists reports whether a tmux session with the exact given name
// exists. It is a package-level helper for callers that need to probe by
// name (e.g. orphan reclamation) rather than via a TmuxSession value.
func SessionExists(cmdExec cmd.Executor, name string) bool {
	return cmdExec.Run(exec.Command("tmux", "has-session", fmt.Sprintf("-t=%s", name))) == nil
}

// KillSession kills a tmux session by exact name. It returns an error if the
// session does not exist (tmux's kill-session exits non-zero for an absent
// session). Callers that need an absent-safe no-op should guard with
// SessionExists first — the orchestrator's reclaim path does exactly that.
func KillSession(cmdExec cmd.Executor, name string) error {
	return cmdExec.Run(exec.Command("tmux", "kill-session", "-t", name))
}

// NewTmuxSession creates a new TmuxSession with the given name and program.
func NewTmuxSession(name string, program string) *TmuxSession {
	return newTmuxSession(name, program, cmd.MakeExecutor())
}

// NewTmuxSessionWithDeps creates a new TmuxSession with a provided command
// executor (the SSH-aware transport seam) for testing.
func NewTmuxSessionWithDeps(name string, program string, cmdExec cmd.Executor) *TmuxSession {
	return newTmuxSession(name, program, cmdExec)
}

func newTmuxSession(name string, programCmd string, cmdExec cmd.Executor) *TmuxSession {
	return &TmuxSession{
		sanitizedName: toBoulezTmuxName(name),
		program:       programCmd,
		adapter:       program.Lookup(programCmd),
		cmdExec:       cmdExec,
	}
}

// Start creates and starts a new tmux session, then attaches to it. Program is the command to run in
// the session (ex. claude). workdir is the git worktree directory.
func (t *TmuxSession) Start(workDir string) error {
	// Check if the session already exists
	if t.DoesSessionExist() {
		return fmt.Errorf("tmux session already exists: %s", t.sanitizedName)
	}

	// Create a new detached tmux session and start the agent in it. This is a
	// one-shot, non-interactive command, so it goes through the host executor
	// (cmdExec) — the SSH-aware channel CapturePaneContent/SendKeys already
	// use — rather than a PTY. A PTY was wrong here: tmux new-session -d is
	// detached and never reads a terminal.
	startCmd := exec.Command("tmux", "new-session", "-d", "-s", t.sanitizedName, "-c", workDir, t.program)
	if err := t.cmdExec.Run(startCmd); err != nil {
		// Cleanup any partially created session if any exists.
		if t.DoesSessionExist() {
			cleanupCmd := exec.Command("tmux", "kill-session", "-t", t.sanitizedName)
			if cleanupErr := t.cmdExec.Run(cleanupCmd); cleanupErr != nil {
				err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
			}
		}
		return fmt.Errorf("error starting tmux session: %w", err)
	}

	// Poll for session existence with exponential backoff. Capture the last
	// has-session failure so the timeout message surfaces the real reason
	// (previously it printed %v of a nil err, masking e.g. an ssh/transport
	// failure or a real tmux error behind a bare "<nil>").
	timeout := time.After(2 * time.Second)
	sleepDuration := 5 * time.Millisecond
	var lastErr error
	for {
		exists, pollErr := t.doesSessionExistWithErr()
		if exists {
			break
		}
		if pollErr != nil {
			lastErr = pollErr
		}
		select {
		case <-timeout:
			if cleanupErr := t.Close(); cleanupErr != nil {
				lastErr = fmt.Errorf("%v (cleanup error: %v)", lastErr, cleanupErr)
			}
			return fmt.Errorf("timed out waiting for tmux session %s: %w", t.sanitizedName, lastErr)
		default:
			time.Sleep(sleepDuration)
			// Exponential backoff up to 50ms max
			if sleepDuration < 50*time.Millisecond {
				sleepDuration *= 2
			}
		}
	}

	// Set history limit to enable scrollback (default is 2000, we'll use 10000 for more history)
	historyCmd := exec.Command("tmux", "set-option", "-t", t.sanitizedName, "history-limit", "10000")
	if err := t.cmdExec.Run(historyCmd); err != nil {
		log.InfoLog.Printf("Warning: failed to set history-limit for session %s: %v", t.sanitizedName, err)
	}

	// Enable mouse scrolling for the session
	mouseCmd := exec.Command("tmux", "set-option", "-t", t.sanitizedName, "mouse", "on")
	if err := t.cmdExec.Run(mouseCmd); err != nil {
		log.InfoLog.Printf("Warning: failed to enable mouse scrolling for session %s: %v", t.sanitizedName, err)
	}

	if err := t.Restore(); err != nil {
		if cleanupErr := t.Close(); cleanupErr != nil {
			return fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
		}
		return fmt.Errorf("error restoring tmux session: %w", err)
	}

	return nil
}

// CheckAndHandleTrustPrompt checks the pane content once for a prompt that the
// agent's adapter knows how to resolve (e.g. a trust or MCP approval prompt)
// and dismisses it if found. Returns true if a prompt was found and handled.
//
// The agent-specific knowledge of *which* strings to look for and *how* to
// dismiss them lives in the program.Adapter; this function is now a generic
// detect-and-resolve loop for any adapter.
func (t *TmuxSession) CheckAndHandleTrustPrompt() bool {
	content, err := t.CapturePaneContent()
	if err != nil {
		return false
	}

	_, prompt := t.adapter.Detect(content)
	if prompt == nil || prompt.Resolve == nil {
		return false
	}
	if err := prompt.Resolve(t); err != nil {
		log.ErrorLog.Printf("could not resolve %s prompt: %v", t.adapter.Name(), err)
		return false
	}
	return true
}

// Restore initializes the status monitor for a pre-existing session (used
// when Start reuses a session that survived a pause, or when the daemon
// rebinds to a session from a previous run). It no longer allocates a PTY —
// the interactive attach is now host.AttachCmd + tea.ExecProcess, which does
// not need a long-lived PTY on the TmuxSession. Keeping a phantom PTY here
// was the second half of the PTY-machinery smell: Restore was allocating a
// tmux-attach PTY with no reader, purely to satisfy the (now-removed) Attach
// path's invariant that ptmx was non-nil.
func (t *TmuxSession) Restore() error {
	t.monitor = newStatusMonitor()
	return nil
}

type statusMonitor struct {
	// Store hashes to save memory.
	prevOutputHash []byte
	// lastChangeAt is the last time the pane content hash changed (i.e. the
	// agent produced output). Used to derive stableFor: if the pane hasn't
	// changed for longer than the threshold, the agent is presumed idle even
	// when no adapter-specific ready signal is present. This is the
	// agent-agnostic fallback that makes boulez work for any harness without
	// a dedicated adapter — the adapter's authoritative Ready/Permission
	// signal always takes priority, but when the adapter is silent (unknown
	// agent, or a known agent whose extension isn't installed), stability is
	// a good-enough proxy for "waiting on input".
	lastChangeAt time.Time
	// captureErrEvery throttles the "error capturing pane content" log in
	// HasUpdated. The daemon poll loop calls HasUpdated every tick; a
	// transiently-unreachable instance would otherwise spam once per tick.
	// Best-effort: a real fix (removing dead instances from the fleet) lives
	// in the kernel; this just keeps the log quiet in the meantime.
	captureErrEvery *log.Every
}

func newStatusMonitor() *statusMonitor {
	return &statusMonitor{
		captureErrEvery: log.NewEvery(60 * time.Second),
		lastChangeAt:    time.Now(),
	}
}

// hash hashes the string.
func (m *statusMonitor) hash(s string) []byte {
	h := sha256.New()
	// TODO: this allocation sucks since the string is probably large. Ideally, we hash the string directly.
	h.Write([]byte(s))
	return h.Sum(nil)
}

// TapEnter sends an enter keystroke to the tmux pane via `tmux send-keys`.
// This goes through the host executor (cmdExec), so it works whether or not a
// PTY is currently attached, and transparently reaches remote (SSH) hosts —
// the same channel CapturePaneContent uses for reading. Sending Enter as a
// named key (rather than a raw 0x0D byte) is equivalent to writing 0x0D to an
// attached PTY: tmux forwards the Enter key to the pane.
func (t *TmuxSession) TapEnter() error {
	cmd := exec.Command("tmux", "send-keys", "-t", t.sanitizedName, "Enter")
	if err := t.cmdExec.Run(cmd); err != nil {
		return fmt.Errorf("error sending enter keystroke: %w", err)
	}
	return nil
}

// TapDAndEnter sends 'D' followed by an enter keystroke to the tmux pane.
// See TapEnter for why this uses `tmux send-keys` rather than a raw PTY write.
func (t *TmuxSession) TapDAndEnter() error {
	cmd := exec.Command("tmux", "send-keys", "-t", t.sanitizedName, "D", "Enter")
	if err := t.cmdExec.Run(cmd); err != nil {
		return fmt.Errorf("error sending D+enter keystroke: %w", err)
	}
	return nil
}

// SendKeys sends the given string to the tmux pane via `tmux send-keys -l`.
// The -l flag makes tmux treat the argument as literal characters rather than
// key names, so a user (or prompt sender) typing the word "Enter" is sent as
// the letters E-n-t-e-r, not the Enter key. This matches the previous raw-PTY
// byte-write behaviour (which also did no key-name interpretation) while
// working without an attached PTY and over SSH — the same channel
// CapturePaneContent uses for reading. Callers that need to submit input
// must call TapEnter (or append an Enter) separately; SendKeys does not
// append a newline.
func (t *TmuxSession) SendKeys(keys string) error {
	cmd := exec.Command("tmux", "send-keys", "-t", t.sanitizedName, "-l", keys)
	if err := t.cmdExec.Run(cmd); err != nil {
		return fmt.Errorf("error sending keys to tmux pane: %w", err)
	}
	return nil
}

// SendKey sends a single named tmux key to the pane via `tmux send-keys`
// (without -l). This is the named-key counterpart to SendKeys' literal text:
// `Enter`, `BSpace`, `Delete`, `Up`, `C-c`, `M-b`, ... — any key tmux's key
// parser knows by name. Used by the TUI's insert mode to forward special keys
// (backspace, arrows, Ctrl+C, Tab, ...) so the agent's own readline/editor
// stays the authority over editing, history, and completion. Same
// host-executor channel as SendKeys, so it works over SSH and without an
// attached PTY.
func (t *TmuxSession) SendKey(name string) error {
	cmd := exec.Command("tmux", "send-keys", "-t", t.sanitizedName, name)
	if err := t.cmdExec.Run(cmd); err != nil {
		return fmt.Errorf("error sending key %q to tmux pane: %w", name, err)
	}
	return nil
}

// HasUpdated checks if the tmux pane content has changed since the last
// tick, and returns the agent's perceived status from the program.Adapter
// plus how long the pane has been stable (stableFor).
//
// Callers branch on the precise Status rather than a lossy "has prompt" bool:
// a definitive StatusReady/StatusPermission from the adapter MUST take
// priority over the content-change heuristic. When an agent finishes a turn
// it emits its ready marker (e.g. Pi's boulez:ready sentinel), which changes the
// pane content; classifying that as "working" just because the pane changed
// would leave a finished agent stuck showing the running spinner forever.
// Agent-specific detection is delegated to program.Adapter; this function
// holds no agent-specific strings.
//
// stableFor is the agent-agnostic fallback signal: the time elapsed since the
// pane content last changed. When the adapter returns StatusWorking or
// StatusUnknown (no authoritative ready signal — e.g. an unknown agent, or a
// known agent whose sentinel extension isn't installed), the caller may treat
// a sufficiently long stableFor as "idle / waiting on input". This keeps
// boulez usable for any harness without a dedicated adapter: the worst case
// is a delayed Ready badge rather than an instance stuck on Running forever.
// A long silent tool run (build/test with no output) can produce a transient
// false Ready, but it self-corrects the moment the tool emits a line.
func (t *TmuxSession) HasUpdated() (updated bool, status program.Status, stableFor time.Duration) {
	content, err := t.CapturePaneContent()
	if err != nil {
		if t.monitor.captureErrEvery.ShouldLog() {
			log.ErrorLog.Printf("error capturing pane content in status monitor: %v", err)
		}
		return false, program.StatusUnknown, 0
	}

	status, _ = t.adapter.Detect(content)
	now := time.Now()
	if !bytes.Equal(t.monitor.hash(content), t.monitor.prevOutputHash) {
		t.monitor.prevOutputHash = t.monitor.hash(content)
		t.monitor.lastChangeAt = now
		return true, status, 0
	}
	return false, status, now.Sub(t.monitor.lastChangeAt)
}

// Close terminates the tmux session and cleans up resources
func (t *TmuxSession) Close() error {
	var errs []error

	cmd := exec.Command("tmux", "kill-session", "-t", t.sanitizedName)
	if err := t.cmdExec.Run(cmd); err != nil {
		// Idempotent: a session that is already gone (e.g. the instance was
		// reconciled to Dead after its tmux session crashed) is a no-op
		// success, not an error. Without this, killing an already-dead
		// instance fails the whole Kill and its record lingers in the fleet
		// forever (the unkillable-zombie regression). Probe with has-session:
		// if definitively gone, swallow the error; only a real kill failure on
		// a live session is surfaced.
		if t.DoesSessionExist() {
			errs = append(errs, fmt.Errorf("error killing tmux session: %w", err))
		}
	}

	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}

	errMsg := "multiple errors occurred during cleanup:"
	for _, err := range errs {
		errMsg += "\n  - " + err.Error()
	}
	return errors.New(errMsg)
}

// SetDetachedSize set the width and height of the session while detached. This makes the
// tmux output conform to the specified shape.
func (t *TmuxSession) SetDetachedSize(width, height int) error {
	return t.updateWindowSize(width, height)
}

// updateWindowSize updates the detached session's window size via
// `tmux resize-window`, routed through the host executor (SSH-aware). The
// previous implementation resized a local PTY (pty.Setsize on t.ptmx), which
// is gone now that interactive attach is tea.ExecProcess — and which never
// worked for a remote instance anyway (the PTY was local; the remote tmux
// pane kept its old size). resize-window operates on the session itself, so
// it works on both transports.
func (t *TmuxSession) updateWindowSize(cols, rows int) error {
	return t.cmdExec.Run(exec.Command("tmux",
		"resize-window", "-t", t.sanitizedName,
		"-x", fmt.Sprintf("%d", cols), "-y", fmt.Sprintf("%d", rows)))
}

// Name returns the sanitized tmux session name used in tmux commands
// (has-session, kill-session, capture-pane, attach-session, ...). Exposed so a
// caller can build an attach command (e.g. host.AttachCmd) without re-deriving
// the sanitization.
func (t *TmuxSession) Name() string {
	return t.sanitizedName
}

func (t *TmuxSession) DoesSessionExist() bool {
	exists, _ := t.doesSessionExistWithErr()
	return exists
}

// doesSessionExistWithErr is the error-returning core of DoesSessionExist. It
// runs `tmux has-session -t=<name>` and returns (true, nil) when the session
// exists, or (false, err) with the real exit error otherwise — so callers that
// care about WHY a session never appeared (the Start poll loop) can surface it
// instead of a bare "<nil>".
func (t *TmuxSession) doesSessionExistWithErr() (bool, error) {
	// Using "-t name" does a prefix match, which is wrong. `-t=` does an exact match.
	existsCmd := exec.Command("tmux", "has-session", fmt.Sprintf("-t=%s", t.sanitizedName))
	if err := t.cmdExec.Run(existsCmd); err != nil {
		return false, err
	}
	return true, nil
}

// CapturePaneContent captures the content of the tmux pane
func (t *TmuxSession) CapturePaneContent() (string, error) {
	// Add -e flag to preserve escape sequences (ANSI color codes)
	cmd := exec.Command("tmux", "capture-pane", "-p", "-e", "-J", "-t", t.sanitizedName)
	output, err := t.cmdExec.Output(cmd)
	if err != nil {
		return "", fmt.Errorf("error capturing pane content: %v", err)
	}
	return string(output), nil
}

// CapturePaneContentWithOptions captures the pane content with additional options
// start and end specify the starting and ending line numbers (use "-" for the start/end of history)
func (t *TmuxSession) CapturePaneContentWithOptions(start, end string) (string, error) {
	// Add -e flag to preserve escape sequences (ANSI color codes)
	cmd := exec.Command("tmux", "capture-pane", "-p", "-e", "-J", "-S", start, "-E", end, "-t", t.sanitizedName)
	output, err := t.cmdExec.Output(cmd)
	if err != nil {
		return "", fmt.Errorf("failed to capture tmux pane content with options: %v", err)
	}
	return string(output), nil
}

// CleanupSessions kills all tmux sessions that start with "session-"
func CleanupSessions(cmdExec cmd.Executor) error {
	// First try to list sessions
	cmd := exec.Command("tmux", "ls")
	output, err := cmdExec.Output(cmd)

	// If there's an error and it's because no server is running, that's fine
	// Exit code 1 typically means no sessions exist
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil // No sessions to clean up
		}
		return fmt.Errorf("failed to list tmux sessions: %v", err)
	}

	re := regexp.MustCompile(fmt.Sprintf(`%s.*:`, TmuxPrefix))
	matches := re.FindAllString(string(output), -1)
	for i, match := range matches {
		matches[i] = match[:strings.Index(match, ":")]
	}

	for _, match := range matches {
		log.InfoLog.Printf("cleaning up session: %s", match)
		if err := cmdExec.Run(exec.Command("tmux", "kill-session", "-t", match)); err != nil {
			return fmt.Errorf("failed to kill tmux session %s: %v", match, err)
		}
	}
	return nil
}
