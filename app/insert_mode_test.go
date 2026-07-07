package app

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yro7/boulez/cmd/cmd_test"
	"github.com/yro7/boulez/config"
	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/session/tmux"
	"github.com/yro7/boulez/ui"
)

// recordingCmdExec is a MockCmdExec that records every Run command's argv, so
// a test can assert that SendKey/SendKeys translated into `tmux send-keys`
// invocations via the host executor (the channel that also works over SSH).
type recordingCmdExec struct {
	mu      sync.Mutex
	ranCmds []string
	hasSess bool // whether has-session reports the session as existing
}

func (r *recordingCmdExec) RunFunc() func(*exec.Cmd) error {
	return func(c *exec.Cmd) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.ranCmds = append(r.ranCmds, strings.Join(c.Args, " "))
		s := strings.Join(c.Args, " ")
		if strings.Contains(s, "has-session") {
			if r.hasSess {
				return nil
			}
			return fmt.Errorf("session does not exist")
		}
		return nil
	}
}

func (r *recordingCmdExec) OutputFunc() func(*exec.Cmd) ([]byte, error) {

// newStartedMockInstance builds an instance that reports as started with the
	return func(c *exec.Cmd) ([]byte, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.ranCmds = append(r.ranCmds, strings.Join(c.Args, " "))
		return []byte(""), nil
	}
}

func (r *recordingCmdExec) sendKeysCmds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.ranCmds {
		if strings.Contains(c, "send-keys") {
			out = append(out, c)
		}
	}
	return out
}

// newStartedMockInstance builds an instance that reports as started with the
// given status, wired to a real *tmux.TmuxSession whose commands are captured
// by a recordingCmdExec. Uses MarkStartedForTest to avoid the full git-
// worktree Start path — SendKey/SendKeys only need started=true and a
// tmux session, not a real worktree. This mirrors how kernel package tests
// construct an in-memory instance (see Instance.MarkStartedForTest).
func newStartedMockInstance(t *testing.T, status session.Status) (*session.Instance, *recordingCmdExec) {
	t.Helper()
	workdir := t.TempDir()
	// minimal git repo (some Instance methods touch git even without Start)
	if err := exec.Command("git", "-C", workdir, "init").Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := exec.Command("git", "-C", workdir, "config", "--local", "user.email", "t@t").Run(); err != nil {
		t.Fatalf("git config: %v", err)
	}
	if err := exec.Command("git", "-C", workdir, "config", "--local", "user.name", "t").Run(); err != nil {
		t.Fatalf("git config: %v", err)
	}

	rec := &recordingCmdExec{hasSess: true}
	cmdExec := cmd_test.MockCmdExec{
		RunFunc:    rec.RunFunc(),
		OutputFunc: rec.OutputFunc(),
	}

	name := fmt.Sprintf("insert-test-%d-%d", time.Now().UnixNano(), time.Now().UnixMicro())
	// Clean slate in case a stale session lingers.
	_ = exec.Command("tmux", "kill-session", "-t", "boulez_"+name).Run()

	inst, err := session.NewInstance(session.InstanceOptions{
		Title:   name,
		Path:    workdir,
		Program: "bash",
		AutoYes: false,
	})
	require.NoError(t, err)

	ts := tmux.NewTmuxSessionWithDeps(name, "bash", cmdExec)
	inst.SetTmuxSession(ts)
	inst.MarkStartedForTest()
	inst.SetStatus(status)
	return inst, rec
}

// newInsertTestHome wires a home with a tabbedWindow and a started instance
// selected, on the Preview tab (the only tab where insert mode is allowed).
func newInsertTestHome(t *testing.T, inst *session.Instance) *home {
	t.Helper()
	spin := spinner.Model{}
	list := ui.NewList(&spin, false)
	list.AddInstance(inst)
	list.SetSelectedInstance(0)
	preview := ui.NewPreviewPane()
	tw := ui.NewTabbedWindow(preview, ui.NewDiffPane(), ui.NewTerminalPane())
	tw.SetInstance(inst)
	return &home{
		ctx:          context.Background(),
		state:        stateDefault,
		appConfig:    config.DefaultConfig(),
		list:         list,
		menu:         ui.NewMenu(),
		tabbedWindow: tw,
		errBox:       ui.NewErrBox(),
	}
}

// TestInsertMode_EnterAndExit verifies: `i` enters insert mode on the Preview
// tab with a started instance; Esc returns to stateDefault.
func TestInsertMode_EnterAndExit(t *testing.T) {
	inst, _ := newStartedMockInstance(t, session.Ready)
	h := newInsertTestHome(t, inst)
	h.keySent = true

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	require.Equal(t, stateInsert, h.state, "i enters insert mode on a Ready instance")
	require.True(t, h.tabbedWindow.IsPreviewInInsertMode(), "preview pane is in insert mode")

	// typing in insert mode must not move the fleet selection or change state
	_, _ = h.handleInsertState(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")})
	require.Equal(t, stateInsert, h.state, "still in insert mode after typing")

	// Esc exits
	_, _ = h.handleInsertState(tea.KeyMsg{Type: tea.KeyEscape})
	require.Equal(t, stateDefault, h.state, "Esc exits insert mode")
	require.False(t, h.tabbedWindow.IsPreviewInInsertMode(), "preview pane left insert mode")
}

// TestInsertMode_RefusedOnPausedInstance verifies the safety gate: i does not
// enter insert mode when the instance cannot receive input.
func TestInsertMode_RefusedOnPausedInstance(t *testing.T) {
	inst, _ := newStartedMockInstance(t, session.Paused)
	h := newInsertTestHome(t, inst)
	h.keySent = true

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	require.Equal(t, stateDefault, h.state, "i must not enter insert mode on a paused instance")
	require.False(t, h.tabbedWindow.IsPreviewInInsertMode())
}

// TestInsertMode_RefusedOnDiffTab verifies insert mode is Preview-only: with
// the Diff tab active, `i` is a no-op (the Terminal tab has its own attach
// flow; duplicating the forwarding there is premature per AGENTS.md).
func TestInsertMode_RefusedOnDiffTab(t *testing.T) {
	inst, _ := newStartedMockInstance(t, session.Ready)
	h := newInsertTestHome(t, inst)
	// Switch to the Diff tab
	h.tabbedWindow.Toggle()
	require.True(t, h.tabbedWindow.IsInDiffTab())
	h.keySent = true

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	require.Equal(t, stateDefault, h.state, "i must not enter insert mode outside the Preview tab")
	require.False(t, h.tabbedWindow.IsPreviewInInsertMode())
}

// TestInsertMode_PureInjection_ForwardsEachKeyImmediately is the pin for the
// refactor's core guarantee: there is NO local text buffer. Each keystroke is
// translated to a `tmux send-keys` argv and dispatched immediately, so the
// agent's own readline/editor (backspace, arrows, history, completion, Ctrl-C)
// is the authority over editing. This is what distinguishes "pure injection"
// from the previous chat-style buffer-then-commit design, which could only
// grow and could not delete.
func TestInsertMode_PureInjection_ForwardsEachKeyImmediately(t *testing.T) {
	inst, rec := newStartedMockInstance(t, session.Ready)
	h := newInsertTestHome(t, inst)
	h.keySent = true

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	require.Equal(t, stateInsert, h.state)

	cases := []struct {
		name   string
		msg    tea.KeyMsg
		wantSub string // substring expected in the recorded send-keys argv
		wantLiteral bool // true => argv uses -l (literal); false => named key
	}{
		{"printable runes forwarded literally",
			tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")},
			"hello", true},
		{"backspace forwarded as BSpace (the deletion case the buffer design could not handle)",
			tea.KeyMsg{Type: tea.KeyBackspace},
			"BSpace", false},
		{"delete forwarded as Delete",
			tea.KeyMsg{Type: tea.KeyDelete},
			"Delete", false},
		{"enter forwarded as the Enter named key (no separate commit step)",
			tea.KeyMsg{Type: tea.KeyEnter},
			"Enter", false},
		{"tab forwarded as Tab",
			tea.KeyMsg{Type: tea.KeyTab},
			"Tab", false},
		{"up arrow forwarded as Up (history navigation in the agent)",
			tea.KeyMsg{Type: tea.KeyUp},
			"Up", false},
		{"down arrow forwarded as Down",
			tea.KeyMsg{Type: tea.KeyDown},
			"Down", false},
		{"left arrow forwarded as Left (cursor movement)",
			tea.KeyMsg{Type: tea.KeyLeft},
			"Left", false},
		{"right arrow forwarded as Right",
			tea.KeyMsg{Type: tea.KeyRight},
			"Right", false},
		{"home forwarded as Home",
			tea.KeyMsg{Type: tea.KeyHome},
			"Home", false},
		{"end forwarded as End",
			tea.KeyMsg{Type: tea.KeyEnd},
			"End", false},
		{"ctrl-u forwarded as C-u (unix line kill — the agent's readline handles it)",
			tea.KeyMsg{Type: tea.KeyCtrlU},
			"C-u", false},
		{"ctrl-w forwarded as C-w (word delete)",
			tea.KeyMsg{Type: tea.KeyCtrlW},
			"C-w", false},
		{"ctrl-c forwarded as C-c (interrupt the agent without leaving insert mode)",
			tea.KeyMsg{Type: tea.KeyCtrlC},
			"C-c", false},
		{"ctrl-r forwarded as C-r (reverse history search)",
			tea.KeyMsg{Type: tea.KeyCtrlR},
			"C-r", false},
		{"alt+b forwarded as M-b (emacs backward-word)",
			tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b"), Alt: true},
			"M-b", false},
	}

	for i, c := range cases {
		before := len(rec.sendKeysCmds())
		_, _ = h.handleInsertState(c.msg)
		after := rec.sendKeysCmds()
		require.Greater(t, len(after), before,
			"%s (%d): key must produce a send-keys call", c.name, i)
		last := after[len(after)-1]
		assert.Contains(t, last, c.wantSub,
			"%s (%d): argv %q must contain %q", c.name, i, last, c.wantSub)
		if c.wantLiteral {
			assert.Contains(t, last, "-l",
				"%s (%d): literal runes must use -l flag, got %q", c.name, i, last)
		} else {
			assert.NotContains(t, last, " -l",
				"%s (%d): named key must NOT use -l flag, got %q", c.name, i, last)
		}
		require.Equal(t, stateInsert, h.state, "%s: stays in insert mode", c.name)
	}

	// No buffer means typing, deleting, then typing again just produces three
	// independent send-keys calls — the agent's readline composes them. This
	// is the regression guard against ever reintroducing a TUI-side buffer.
	cmds := rec.sendKeysCmds()
	require.GreaterOrEqual(t, len(cmds), len(cases),
		"every key produced its own send-keys call (no batching/commit step)")
}

// TestInsertMode_EscNotForwarded verifies Esc is intercepted by the TUI to
// exit insert mode and is NEVER forwarded to the agent (vim contract: Esc is
// the modal exit, not a character).
func TestInsertMode_EscNotForwarded(t *testing.T) {
	inst, rec := newStartedMockInstance(t, session.Ready)
	h := newInsertTestHome(t, inst)
	h.keySent = true

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	require.Equal(t, stateInsert, h.state)

	before := len(rec.sendKeysCmds())
	_, _ = h.handleInsertState(tea.KeyMsg{Type: tea.KeyEscape})
	require.Equal(t, stateDefault, h.state, "Esc exits insert mode")
	require.Len(t, rec.sendKeysCmds(), before, "Esc must not be forwarded to the agent")
}

// TestInsertMode_QDoesNotQuitWhileInserting verifies the modal collision fix:
// `q` is forwarded to the agent as a literal character in insert mode (so a
// user typing "quit" into the agent does not quit the TUI), but still quits
// from stateDefault.
func TestInsertMode_QDoesNotQuitWhileInserting(t *testing.T) {
	inst, rec := newStartedMockInstance(t, session.Ready)
	h := newInsertTestHome(t, inst)

	// Enter insert mode
	h.keySent = true
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	require.Equal(t, stateInsert, h.state)

	// 'q' in insert mode must be forwarded to the agent, not quit the TUI.
	_, cmd := h.handleInsertState(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	require.Equal(t, stateInsert, h.state, "q does not quit while inserting")
	require.Nil(t, cmd, "no quit command issued while inserting")
	cmds := rec.sendKeysCmds()
	require.NotEmpty(t, cmds, "q must be forwarded to the agent")
	assert.Contains(t, cmds[len(cmds)-1], " -l q", "q is forwarded as a literal character")
}

// TestInsertMode_AutoExitWhenInstancePauses verifies the safety net: if the
// instance becomes unsendable mid-insert (paused/killed), the next key drops
// back to stateDefault instead of forwarding undeliverable input.
func TestInsertMode_AutoExitWhenInstancePauses(t *testing.T) {
	inst, rec := newStartedMockInstance(t, session.Ready)
	h := newInsertTestHome(t, inst)
	h.keySent = true

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	require.Equal(t, stateInsert, h.state)

	// Instance is paused out from under us (e.g. user ran `boulez ctl pause`
	// from another terminal, or the kernel reconciled it).
	inst.SetStatus(session.Paused)

	// Any key in insert mode now triggers the safety net.
	before := len(rec.sendKeysCmds())
	_, _ = h.handleInsertState(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	require.Equal(t, stateDefault, h.state, "auto-exit insert mode when instance becomes unsendable")
	require.False(t, h.tabbedWindow.IsPreviewInInsertMode())
	require.Len(t, rec.sendKeysCmds(), before, "no key forwarded after the instance became unsendable")
}
