package ui

import (
	"claude-squad/host"
	"claude-squad/session"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"
)

func newTestList(titles ...string) *List {
	s := spinner.New()
	l := NewList(&s, false)
	for _, t := range titles {
		inst, _ := session.NewInstance(session.InstanceOptions{
			Title:   t,
			Path:    ".",
			Program: "echo",
		})
		l.AddInstance(inst)
	}
	return l
}

func TestRepoBadgeColorInPaletteAndStable(t *testing.T) {
	name := "some-repo"
	c := repoBadgeColor(name)
	require.Contains(t, repoBadgePalette, c)
	require.Equal(t, c, repoBadgeColor(name))
}

func TestRepoBadgeColorEmptyFallback(t *testing.T) {
	require.Equal(t, repoBadgePalette[0], repoBadgeColor(""))
}

func TestRepoBadgeRendersName(t *testing.T) {
	// The badge must embed the repo name so it is readable in the list.
	out := repoBadge("my-repo", lipgloss.Color("#000000"))
	require.Contains(t, out, "[my-repo]")
}

func TestEnvBadgeLocalIsHidden(t *testing.T) {
	// The local host is implicit (the machine running cs2); it must not render
	// a badge, so a local-only list stays uncluttered.
	require.Equal(t, "", envBadge(host.LocalAlias, lipgloss.Color("#000000")))
	require.Equal(t, "", envBadge("", lipgloss.Color("#000000")))
}

func TestEnvBadgeRemoteRendersAlias(t *testing.T) {
	out := envBadge("dev-machine", lipgloss.Color("#000000"))
	require.Contains(t, out, "[dev-machine]")
	// A remote host keeps its badge even with no background (non-selected row).
	require.NotEmpty(t, envBadge("dev-machine", nil))
}

func TestMoveUp(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(1) // select "b"

	moved := l.MoveUp()
	require.True(t, moved)
	require.Equal(t, 0, l.selectedIdx)
	require.Equal(t, "b", l.items[0].Title)
	require.Equal(t, "a", l.items[1].Title)
	require.Equal(t, "c", l.items[2].Title)
}

func TestMoveUp_AtTop(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(0)

	moved := l.MoveUp()
	require.False(t, moved)
	require.Equal(t, 0, l.selectedIdx)
	require.Equal(t, "a", l.items[0].Title)
}

func TestMoveDown(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(1) // select "b"

	moved := l.MoveDown()
	require.True(t, moved)
	require.Equal(t, 2, l.selectedIdx)
	require.Equal(t, "a", l.items[0].Title)
	require.Equal(t, "c", l.items[1].Title)
	require.Equal(t, "b", l.items[2].Title)
}

func TestMoveDown_AtBottom(t *testing.T) {
	l := newTestList("a", "b", "c")
	l.SetSelectedInstance(2)

	moved := l.MoveDown()
	require.False(t, moved)
	require.Equal(t, 2, l.selectedIdx)
	require.Equal(t, "c", l.items[2].Title)
}

func TestMoveWithSingleItem(t *testing.T) {
	l := newTestList("only")
	l.SetSelectedInstance(0)

	require.False(t, l.MoveUp())
	require.False(t, l.MoveDown())
}
