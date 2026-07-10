// Package notify is the seam for delivering instance events to the user.
//
// It is the daemon-side counterpart to the kernel's status authority: the
// daemon already detects status transitions (Running → Ready) in its poll
// loop — this package is how that transition reaches the user's attention.
//
// Today only Desktop is implemented (osascript / notify-send). The seam
// exists so that future channels (webhook, custom command) are one type
// here, not a scatter of shell-outs across the daemon. One adapter means a
// hypothetical seam; we only build the second when a real second channel is
// needed (YAGNI until then).
package notify

import (
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/yro7/boulez/log"
)

// EventKind is the kind of instance event the daemon observed.
type EventKind int

const (
	// InstanceReady fires when an instance transitions to Ready: an agent
	// finished a turn and is waiting for input. This is the canonical
	// "ping the user, the instance is done" event.
	InstanceReady EventKind = iota
)

// InstanceInfo is the minimal, self-contained description of the instance an
// event is about. It is a value (not a pointer to the live Instance) so the
// event can be logged, queued, or handed to a goroutine without holding the
// kernel lock or aliasing mutable instance state.
type InstanceInfo struct {
	ID    string
	Title string
	// Host is the logical host name (local alias or ssh alias) the instance
	// runs on, from host.Host.Name(). Lets a future notifier surface "remote
	// instance finished" distinctly.
	Host string
}

// Event describes one observable instance transition delivered to the user.
type Event struct {
	Kind     EventKind
	Instance InstanceInfo
	At       time.Time
}

// Notifier delivers an Event to the user. Best-effort: implementations MUST
// NOT propagate errors to the caller — the daemon's poll loop calls Notify
// and a notification failure must never break status reconciliation. Log and
// move on. Notify may block briefly (it may shell out), so callers should
// avoid holding locks across it; the Desktop implementation shells out in a
// goroutine precisely so the poll loop is unaffected.
type Notifier interface {
	Notify(Event)
}

// Noop is the zero-value Notifier: notifications disabled. Used when
// cfg.NotifyOnReady is off, so the poll loop code path is identical whether
// or not notifications are on (no nil check at the call site).
type Noop struct{}

// Notify silently discards the event.
func (Noop) Notify(Event) {}

// Desktop notifies via the platform's native notification system:
// osascript -e 'display notification ...' on macOS, notify-send on Linux.
// Unsupported platforms silently skip (the AGENTS.md PII discipline forbids
// leaking provider/account names; here the body carries only the instance
// title, which is user-authored, so that's fine).
//
// The shell-out runs in a goroutine so it never blocks the daemon's poll
// loop — a hung osascript (rare, but possible against a wedged WindowServer)
// must not stall status reconciliation for every other instance.
type Desktop struct{}

// Notify builds and fires the platform notification. Best-effort.
func (Desktop) Notify(e Event) {
	if e.Kind != InstanceReady {
		return
	}
	title, body := readyMessage(e.Instance)
	go func() {
		c, err := buildCommand(title, body)
		if err != nil {
			return // unsupported platform, silently skip
		}
		if err := c.Run(); err != nil {
			log.WarningLog.Printf("notify ready failed for %s: %v", e.Instance.Title, err)
		}
	}()
}

// readyMessage renders the notification title and body for an InstanceReady
// event. Extracted from the shell-out so it is independently testable.
func readyMessage(i InstanceInfo) (title, body string) {
	title = fmt.Sprintf("boulez: %s ready", i.Title)
	body = fmt.Sprintf("Instance '%s' finished and is waiting for input.", i.Title)
	return title, body
}

// buildCommand constructs the platform-specific notification command. Returns
// an error on unsupported platforms (the caller treats that as a silent skip,
// never a hard failure). Extracted so the platform branching is testable
// without actually firing a notification.
func buildCommand(title, body string) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("osascript", "-e",
			fmt.Sprintf("display notification %q with title %q", body, title)), nil
	case "linux":
		return exec.Command("notify-send", title, body), nil
	default:
		return nil, fmt.Errorf("notify: unsupported platform %s", runtime.GOOS)
	}
}
