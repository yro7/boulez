package overlay

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ConfirmationOverlay represents a confirmation dialog overlay
type ConfirmationOverlay struct {
	// Whether the overlay has been dismissed
	Dismissed bool
	// Message to display in the overlay
	message string
	// Width of the overlay
	width int
	// OnConfirm is invoked when the user confirms (presses the confirm key).
	// It returns a tea.Cmd that the caller dispatches in its Update loop,
	// so confirm actions can run asynchronously and surface their outcome
	// (success/error) back to the program. Returning nil is a valid no-op.
	OnConfirm func() tea.Cmd
	// OnCancel is invoked when the user cancels (cancel key or esc). Same
	// contract as OnConfirm.
	OnCancel func() tea.Cmd
	// Custom confirm key (defaults to 'y')
	ConfirmKey string
	// Custom cancel key (defaults to 'n')
	CancelKey string
	// Custom styling options
	borderColor lipgloss.Color
}

// NewConfirmationOverlay creates a new confirmation dialog overlay with the given message
func NewConfirmationOverlay(message string) *ConfirmationOverlay {
	return &ConfirmationOverlay{
		Dismissed:   false,
		message:     message,
		width:       50, // Default width
		ConfirmKey:  "y",
		CancelKey:   "n",
		borderColor: lipgloss.Color("#de613e"), // Red color for confirmations
	}
}

// HandleKeyPress processes a key press and updates the state. It returns
// (closed, cmd): closed is true if the overlay should be dismissed, and cmd
// is the tea.Cmd returned by the confirm/cancel callback (nil when there is
// no callback or the overlay stays open). The caller is responsible for
// dispatching cmd so the action's outcome reaches the Update loop — this is
// what lets a confirm action run a long-running operation and report its
// result back (previously the returned Cmd was discarded).
func (c *ConfirmationOverlay) HandleKeyPress(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch msg.String() {
	case c.ConfirmKey:
		c.Dismissed = true
		var cmd tea.Cmd
		if c.OnConfirm != nil {
			cmd = c.OnConfirm()
		}
		return true, cmd
	case c.CancelKey, "esc":
		c.Dismissed = true
		var cmd tea.Cmd
		if c.OnCancel != nil {
			cmd = c.OnCancel()
		}
		return true, cmd
	default:
		// Ignore other keys in confirmation state
		return false, nil
	}
}

// Render renders the confirmation overlay
func (c *ConfirmationOverlay) Render(opts ...WhitespaceOption) string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c.borderColor).
		Padding(1, 2).
		Width(c.width)

	// Add the confirmation instructions
	content := c.message + "\n\n" +
		"Press " + lipgloss.NewStyle().Bold(true).Render(c.ConfirmKey) + " to confirm, " +
		lipgloss.NewStyle().Bold(true).Render(c.CancelKey) + " or " +
		lipgloss.NewStyle().Bold(true).Render("esc") + " to cancel"

	// Apply the border style and return
	return style.Render(content)
}

// SetWidth sets the width of the confirmation overlay
func (c *ConfirmationOverlay) SetWidth(width int) {
	c.width = width
}

// SetBorderColor sets the border color of the confirmation overlay
func (c *ConfirmationOverlay) SetBorderColor(color lipgloss.Color) {
	c.borderColor = color
}

// SetConfirmKey sets the key used to confirm the action
func (c *ConfirmationOverlay) SetConfirmKey(key string) {
	c.ConfirmKey = key
}

// SetCancelKey sets the key used to cancel the action
func (c *ConfirmationOverlay) SetCancelKey(key string) {
	c.CancelKey = key
}
