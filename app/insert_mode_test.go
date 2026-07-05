package app

import (
	"context"
	"fmt"
	"os"
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
// a test can assert that SendKeys/SendEnter translated into `tmux send-keys`
// invocations via the host executor (the channel that also works over SSH).
type recordingCmdExec struct {
	mu       sync.Mutex
	ranCmds  []string
	hasSess  bool // whether has-session reports the session as existing
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

// mockPtyFactoryForApp is a minimal pty factory for app tests: it returns a
// temp file so Start's PTY bookkeeping doesn't panic. Modeled on
// ui.MockPtyFactory but kept local to app to avoid an import cycle.
type mockPtyFactoryForApp struct {
	t *testing.T
}

func (m *mockPtyFactoryForApp) Start(c *exec.Cmd) (*os.File, error) {
	f, err := os.CreateTemp(m.t.TempDir(), "pty-*")
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (m *mockPtyFactoryForApp) Close() {}

// newStartedMockInstance builds an instance that reports as started with the
// given status, wired to a real *tmux.TmuxSession whose commands are captured
// by a recordingCmdExec. Uses MarkStartedForTest to avoid the full git-
// worktree Start path — SendKeys/SendEnter only need started=true and a
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

	ptyFactory := &mockPtyFactoryForApp{t: t}
	ts := tmux.NewTmuxSessionWithDeps(name, "bash", ptyFactory, cmdExec)
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
	}
}

// TestInsertMode_EnterAndExit verifies: `i` enters insert mode on the Preview
// tab with a started instance; Esc returns to stateDefault and clears the
// buffer.
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

	// Esc exits and clears the buffer
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
// flow; duplicating the buffer there is premature per AGENTS.md).
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

// TestInsertMode_EnterSendsKeysAndStaysInMode verifies that typing + Enter
// forwards the buffered text via `tmux send-keys -l` and submits it via a
// separate `tmux send-keys Enter`, then stays in insert mode for the next line.
func TestInsertMode_EnterSendsKeysAndStaysInMode(t *testing.T) {
	inst, rec := newStartedMockInstance(t, session.Ready)
	h := newInsertTestHome(t, inst)
	h.keySent = true

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	require.Equal(t, stateInsert, h.state)

	// type "hello"
	_, _ = h.handleInsertState(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	// commit with Enter
	_, _ = h.handleInsertState(tea.KeyMsg{Type: tea.KeyEnter})

	sendCmds := rec.sendKeysCmds()
	require.NotEmpty(t, sendCmds, "Enter must forward the buffer to tmux send-keys")
	assert.Contains(t, sendCmds[0], "send-keys", "first send is a send-keys")
	assert.Contains(t, sendCmds[0], "-l", "SendKeys uses literal -l flag")
	assert.Contains(t, sendCmds[0], "hello", "buffered text forwarded literally")
	require.Len(t, sendCmds, 2, "SendKeys + SendEnter (two send-keys calls)")
	assert.Contains(t, sendCmds[1], "Enter", "SendEnter submits via the Enter key name")

	require.Equal(t, stateInsert, h.state, "Enter stays in insert mode (Esc exits)")
}

// TestInsertMode_QDoesNotQuitWhileInserting verifies the modal collision fix:
// `q` is a literal character in insert mode (so a user typing "quit" into the
// agent does not quit the TUI), but still quits from stateDefault.
func TestInsertMode_QDoesNotQuitWhileInserting(t *testing.T) {
	inst, _ := newStartedMockInstance(t, session.Ready)
	h := newInsertTestHome(t, inst)

	// Enter insert mode
	h.keySent = true
	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	require.Equal(t, stateInsert, h.state)

	// 'q' in insert mode must accumulate, not quit: the handleQuit guard is
	// `(q || ctrl+c) && state != stateInsert`.
	_, cmd := h.handleInsertState(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	require.Equal(t, stateInsert, h.state, "q does not quit while inserting")
	require.Nil(t, cmd, "no quit command issued while inserting")
}

// TestInsertMode_AutoExitWhenInstancePauses verifies the safety net: if the
// instance becomes unsendable mid-insert (paused/killed), the next key drops
// back to stateDefault instead of buffering undeliverable input.
func TestInsertMode_AutoExitWhenInstancePauses(t *testing.T) {
	inst, _ := newStartedMockInstance(t, session.Ready)
	h := newInsertTestHome(t, inst)
	h.keySent = true

	_, _ = h.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	require.Equal(t, stateInsert, h.state)

	// Instance is paused out from under us (e.g. user ran `boulez ctl pause`
	// from another terminal, or the kernel reconciled it).
	inst.SetStatus(session.Paused)

	// Any key in insert mode now triggers the safety net.
	_, _ = h.handleInsertState(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	require.Equal(t, stateDefault, h.state, "auto-exit insert mode when instance becomes unsendable")
	require.False(t, h.tabbedWindow.IsPreviewInInsertMode())
}
