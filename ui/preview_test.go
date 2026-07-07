package ui

import (
	"fmt"
	"github.com/yro7/boulez/cmd/cmd_test"
	"github.com/yro7/boulez/log"
	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/session/tmux"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testSetup holds common test setup data
type testSetup struct {
	workdir     string
	instance    *session.Instance
	sessionName string
	cleanupFn   func()
}

// setupTestEnvironment creates a common test environment with git repo and instance
func setupTestEnvironment(t *testing.T, cmdExec cmd_test.MockCmdExec) *testSetup {
	t.Helper()

	// Initialize logging
	log.Initialize(false)

	// Set up a temp working directory
	workdir := t.TempDir()

	// Initialize git repository
	setupGitRepo(t, workdir)

	// Create unique session name
	random := time.Now().UnixNano() % 10000000
	sessionName := fmt.Sprintf("test-preview-%s-%d-%d", t.Name(), time.Now().UnixNano(), random)

	// Clean up any existing tmux session
	cleanupCmd := exec.Command("tmux", "kill-session", "-t", "boulez_"+sessionName)
	_ = cleanupCmd.Run() // Ignore errors if session doesn't exist

	// Create instance
	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   sessionName,
		Path:    workdir,
		Program: "bash",
		AutoYes: false,
	})
	require.NoError(t, err)

	// Set up tmux session with mocks
	tmuxSession := tmux.NewTmuxSessionWithDeps(sessionName, "bash", cmdExec)
	instance.SetTmuxSession(tmuxSession)

	// Start the tmux session
	err = instance.Start(true)
	require.NoError(t, err)

	// Create cleanup function
	cleanupFn := func() {
		if instance != nil {
			_ = instance.Kill() // Ignore errors during cleanup
		}
		log.Close()
	}

	return &testSetup{
		workdir:     workdir,
		instance:    instance,
		sessionName: sessionName,
		cleanupFn:   cleanupFn,
	}
}

// setupGitRepo initializes a git repository in the given directory
func setupGitRepo(t *testing.T, workdir string) {
	t.Helper()

	// Initialize git repository
	initCmd := exec.Command("git", "init")
	initCmd.Dir = workdir
	err := initCmd.Run()
	require.NoError(t, err)

	// Create basic git config (local to this repo only)
	configCmd := exec.Command("git", "config", "--local", "user.email", "test@example.com")
	configCmd.Dir = workdir
	err = configCmd.Run()
	require.NoError(t, err)

	configCmd = exec.Command("git", "config", "--local", "user.name", "Test User")
	configCmd.Dir = workdir
	err = configCmd.Run()
	require.NoError(t, err)

	// Create and commit a test file
	testFile := filepath.Join(workdir, "test.txt")
	err = os.WriteFile(testFile, []byte("test content"), 0644)
	require.NoError(t, err)

	addCmd := exec.Command("git", "add", "test.txt")
	addCmd.Dir = workdir
	err = addCmd.Run()
	require.NoError(t, err)

	commitCmd := exec.Command("git", "commit", "-m", "initial commit")
	commitCmd.Dir = workdir
	err = commitCmd.Run()
	require.NoError(t, err)
}

// TestPreviewScrolling tests the scrolling functionality in the preview pane
func TestPreviewScrolling(t *testing.T) {
	// Track what commands were executed and their order
	var executedCommands []string
	inCopyMode := false
	scrollPosition := 0 // 0 = bottom, positive = scrolled up
	sessionCreated := false

	// Create test content with line numbers for scrolling
	const numLines = 100
	lines := make([]string, numLines+1)
	lines[0] = "$ seq 100" // Command that was run
	for i := 1; i <= numLines; i++ {
		lines[i] = fmt.Sprintf("%d", i)
	}
	fullContent := strings.Join(lines, "\n")

	// Mock command execution
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			cmdStr := cmd.String()
			executedCommands = append(executedCommands, cmdStr)

			// Handle tmux session creation and existence checking
			if strings.Contains(cmdStr, "has-session") {
				if sessionCreated {
					return nil // Session exists
				} else {
					return fmt.Errorf("session does not exist")
				}
			}

			// Handle session creation
			if strings.Contains(cmdStr, "new-session") {
				sessionCreated = true
				return nil
			}

			// Handle attach-session
			if strings.Contains(cmdStr, "attach-session") {
				return nil
			}

			// Handle copy mode commands
			if strings.Contains(cmdStr, "copy-mode") {
				inCopyMode = true
			}
			if strings.Contains(cmdStr, "send-keys") && strings.Contains(cmdStr, "q") {
				inCopyMode = false
				scrollPosition = 0 // Reset position when exiting copy mode
			}
			if strings.Contains(cmdStr, "send-keys") && strings.Contains(cmdStr, "Up") {
				if inCopyMode {
					scrollPosition++
				}
			}
			if strings.Contains(cmdStr, "send-keys") && strings.Contains(cmdStr, "Down") {
				if inCopyMode && scrollPosition > 0 {
					scrollPosition--
				}
			}

			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			cmdStr := cmd.String()

			// Handle capture-pane commands
			if strings.Contains(cmdStr, "capture-pane") {
				// Check if this is a request for cursor position
				if strings.Contains(cmdStr, "display-message") && strings.Contains(cmdStr, "copy_cursor_y") {
					var buf []byte
					buf = fmt.Appendf(buf, "%d", scrollPosition)
					return buf, nil
				}

				// Check if this is a copy mode capture with full history (-S -)
				if strings.Contains(cmdStr, "-S -") {
					// Always return the full content for PreviewFullHistory
					return []byte(fullContent), nil
				}

				// Regular capture for normal preview mode - show the last 20 lines
				const visibleLines = 20
				startLine := max(0, numLines+1-visibleLines)
				visibleContent := strings.Join(lines[startLine:], "\n")
				return []byte(visibleContent), nil
			}

			return []byte(""), nil
		},
	}

	// Setup test environment
	setup := setupTestEnvironment(t, cmdExec)
	defer setup.cleanupFn()

	// Simulate running a command that produces lots of output
	err := setup.instance.SendKeys("seq 100")
	require.NoError(t, err)
	err = setup.instance.SendKeys("") // Simulate pressing Enter
	require.NoError(t, err)

	// Create the preview pane
	previewPane := NewPreviewPane()
	previewPane.SetSize(80, 30) // Set reasonable size for testing

	// Step 1: Check initial content - should show normal preview mode
	err = previewPane.UpdateContent(setup.instance)
	require.NoError(t, err)

	// Verify we're not in scrolling mode initially
	require.False(t, previewPane.isScrolling, "Should not be in scrolling mode initially")

	// Step 2: Check that PreviewFullHistory returns all content
	fullHistory, err := setup.instance.PreviewFullHistory()
	require.NoError(t, err)

	// Verify that the full history contains both the command and early output
	require.Contains(t, fullHistory, "$ seq 100", "Full history should contain the command")
	require.Contains(t, fullHistory, "1", "Full history should contain earliest output")

	// Step 3: Enter scroll mode
	err = previewPane.ScrollUp(setup.instance)
	require.NoError(t, err)

	// Verify we entered scrolling mode
	require.True(t, previewPane.isScrolling, "Should be in scrolling mode after ScrollUp")

	// Step 4: Get the content directly from the viewport
	viewportContent := previewPane.viewport.View()
	t.Logf("Viewport content: %q", viewportContent)

	// With proper implementation, the viewport should have the full history content
	// Note: The viewport will be positioned at the bottom initially, so we need to scroll up

	// Step 5: Scroll up multiple times to get to the top
	for range 50 {
		err = previewPane.ScrollUp(setup.instance)
		require.NoError(t, err)
	}

	// Now get the viewport content after scrolling up
	viewportAfterScrollUp := previewPane.viewport.View()
	t.Logf("Viewport after scrolling up: %q", viewportAfterScrollUp)

	// Step 6: Scroll down multiple times
	for range 25 {
		err = previewPane.ScrollDown(setup.instance)
		require.NoError(t, err)
	}

	// Get updated viewport content after scrolling down
	viewportAfterScrollDown := previewPane.viewport.View()
	t.Logf("Viewport after scrolling down: %q", viewportAfterScrollDown)

	// Step 7: Reset to normal mode
	err = previewPane.ResetToNormalMode(setup.instance)
	require.NoError(t, err)

	// Verify we exited scrolling mode
	require.False(t, previewPane.isScrolling, "Should not be in scrolling mode after reset")
}

// TestPreviewContentWithoutScrolling tests that the preview pane correctly displays content
// for a new instance without requiring scrolling
func TestPreviewContentWithoutScrolling(t *testing.T) {
	// Create test content
	expectedContent := "$ echo test\ntest"

	// Track session creation state
	sessionCreated := false

	// Mock command execution
	cmdExec := cmd_test.MockCmdExec{
		RunFunc: func(cmd *exec.Cmd) error {
			cmdStr := cmd.String()

			// Handle tmux session creation and existence checking
			if strings.Contains(cmdStr, "has-session") {
				if sessionCreated {
					return nil // Session exists
				} else {
					return fmt.Errorf("session does not exist")
				}
			}

			// Handle session creation
			if strings.Contains(cmdStr, "new-session") {
				sessionCreated = true
				return nil
			}

			return nil
		},
		OutputFunc: func(cmd *exec.Cmd) ([]byte, error) {
			cmdStr := cmd.String()

			// Handle capture-pane commands for normal preview
			if strings.Contains(cmdStr, "capture-pane") {
				// Return our test content for normal preview
				return []byte(expectedContent), nil
			}

			return []byte(""), nil
		},
	}

	// Setup test environment
	setup := setupTestEnvironment(t, cmdExec)
	defer setup.cleanupFn()

	// Create the preview pane
	previewPane := NewPreviewPane()
	previewPane.SetSize(80, 30) // Set reasonable size for testing

	// Update the preview content (this should display the content without scrolling)
	err := previewPane.UpdateContent(setup.instance)
	require.NoError(t, err)

	// Verify we're not in scrolling mode
	require.False(t, previewPane.isScrolling, "Should not be in scrolling mode")

	// Verify that the preview state is not in fallback mode
	require.False(t, previewPane.previewState.fallback, "Preview should not be in fallback mode")

	// Verify that the preview state contains the expected content
	require.Equal(t, expectedContent, previewPane.previewState.text, "Preview state should contain the expected content")

	// Verify the rendered string contains the content
	renderedString := previewPane.String()
	require.Contains(t, renderedString, "test", "Rendered preview should contain the test content")
}

// Helper function for max
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// --- Insert mode tests ---

// TestSetLiveError_ReplacesStaleLoadingFallback is the regression test for the
// "stuck on Setting up workspace..." bug: when an instance transitions Loading →
// Running and the first live capture ERRORS (the canonical case is an
// unreachable remote host — every `ssh <alias> tmux capture-pane` fails), the
// pane MUST NOT stay on the Loading fallback. Previously the error path only
// surfaced to the error box and never touched previewState, so a Running-but-
// unreachable instance showed "Setting up workspace..." forever — misleading
// the user into thinking the instance was still booting. SetLiveError replaces
// the stale fallback with a connectivity message; the next tick retries the
// capture (the single-flight guard is cleared by the caller), so the pane
// self-heals when the host comes back.
func TestSetLiveError_ReplacesStaleLoadingFallback(t *testing.T) {
	p := NewPreviewPane()
	p.SetSize(80, 30)

	// Simulate the Loading fallback left over from the spawn boot phase.
	p.setFallbackState("Setting up workspace...")
	require.True(t, p.previewState.fallback)
	require.Contains(t, p.String(), "Setting up workspace...")

	// The instance is now Running, but the capture errored (unreachable host).
	// SetLiveError must replace the stale Loading fallback, not leave it.
	p.SetLiveError(nil, fmt.Errorf("ssh: Could not resolve hostname dev-machine"))

	rendered := p.String()
	require.Contains(t, rendered, "Preview unavailable", "error fallback replaces the stale Loading message")
	require.NotContains(t, rendered, "Setting up workspace...", "stale Loading fallback must be cleared")
}

// TestSetLiveError_RespectsScrollMode mirrors SetLiveContent's contract: a
// capture error that lands while the user is scrolling the scrollback must
// not clobber the scroll viewport. The error is dropped (the next tick after
// the user exits scroll mode will re-surface it).
func TestSetLiveError_RespectsScrollMode(t *testing.T) {
	p := NewPreviewPane()
	p.SetSize(80, 30)
	p.isScrolling = true
	p.viewport.SetContent("scrollback content\nESC to exit scroll mode")

	p.SetLiveError(nil, fmt.Errorf("boom"))

	require.True(t, p.isScrolling, "scroll mode preserved")
	require.Contains(t, p.viewport.View(), "scrollback content", "scroll viewport not clobbered by the error")
	require.NotContains(t, p.String(), "Preview unavailable")
}

// TestInsertMode_BannerShown verifies the rendered String() shows the
// -- INSERT -- banner when in insert mode, and reserves space for it below
// the agent output. There is no `> ` prompt line anymore: insert mode is a
// pure injection passthrough (keys go straight to the agent's tmux pane), so
// the agent's own prompt — visible in the captured pane content — is the only
// prompt. The TUI holds no text buffer.
func TestInsertMode_BannerShown(t *testing.T) {
	p := NewPreviewPane()
	p.SetSize(80, 30)
	p.previewState = previewState{fallback: false, text: "agent output line"}

	p.EnterInsertMode()
	require.True(t, p.IsInInsertMode(), "should be in insert mode after Enter")

	rendered := p.String()
	require.Contains(t, rendered, "INSERT", "banner visible in insert mode")
	require.Contains(t, rendered, "agent output line", "agent output still visible above the banner")
	require.NotContains(t, rendered, "> ", "no prompt line — the agent's own prompt is the authority")
}

// TestInsertMode_ExitClearsFlag verifies Esc's counterpart: ExitInsertMode
// leaves insert mode.
func TestInsertMode_ExitClearsFlag(t *testing.T) {
	p := NewPreviewPane()
	p.SetSize(80, 30)

	p.EnterInsertMode()
	p.ExitInsertMode()

	require.False(t, p.IsInInsertMode(), "exited after Exit")
}

// TestInsertMode_EnterIsIdempotent verifies that re-entering insert mode while
// already in it is harmless (no buffer to reset, just a flag).
func TestInsertMode_EnterIsIdempotent(t *testing.T) {
	p := NewPreviewPane()
	p.SetSize(80, 30)

	p.EnterInsertMode()
	p.EnterInsertMode()
	require.True(t, p.IsInInsertMode())
}
