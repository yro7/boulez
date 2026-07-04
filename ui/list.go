package ui

import (
	"claude-squad/host"
	"claude-squad/log"
	"claude-squad/program"
	"claude-squad/session"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

const readyIcon = "● "
const pausedIcon = "⏸ "

var readyStyle = lipgloss.NewStyle().
	Foreground(lipgloss.AdaptiveColor{Light: "#51bd73", Dark: "#51bd73"})

var addedLinesStyle = lipgloss.NewStyle().
	Foreground(lipgloss.AdaptiveColor{Light: "#51bd73", Dark: "#51bd73"})

var removedLinesStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#de613e"))

var pausedStyle = lipgloss.NewStyle().
	Foreground(lipgloss.AdaptiveColor{Light: "#888888", Dark: "#888888"})

var titleStyle = lipgloss.NewStyle().
	Padding(1, 1, 0, 1).
	Foreground(lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#dddddd"})

var listDescStyle = lipgloss.NewStyle().
	Padding(0, 1, 1, 1).
	Foreground(lipgloss.AdaptiveColor{Light: "#A49FA5", Dark: "#777777"})

var selectedTitleStyle = lipgloss.NewStyle().
	Padding(1, 1, 0, 1).
	Background(lipgloss.Color("#dde4f0")).
	Foreground(lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#1a1a1a"})

var selectedDescStyle = lipgloss.NewStyle().
	Padding(0, 1, 1, 1).
	Background(lipgloss.Color("#dde4f0")).
	Foreground(lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#1a1a1a"})

var mainTitle = lipgloss.NewStyle().
	Background(lipgloss.Color("62")).
	Foreground(lipgloss.Color("230"))

var autoYesStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("#dde4f0")).
	Foreground(lipgloss.Color("#1a1a1a"))

// programBadgeColors maps an adapter name to a badge foreground color. Keeps
// the list visually scannable: at a glance you see which agent runs where.
var programBadgeColors = map[string]string{
	"pi":     "#7D56F4", // purple
	"claude": "#FF6B35", // orange
	"aider":  "#36CFC9", // teal
	"gemini": "#4285F4", // blue
	"noop":   "#888888", // grey (unknown agent)
}

// programBadge renders a small colored [name] pill for the agent running in
// the given program command. Uses program.Lookup so the label is the same one
// the detection seam uses (program.Adapter.Name()).
func programBadge(programCmd string) string {
	name := program.Lookup(programCmd).Name()
	color := programBadgeColors[name]
	if color == "" {
		color = programBadgeColors["noop"]
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(color)).
		Render(fmt.Sprintf("[%s]", name))
}

// envBadgeColor is the color for the environment (host) badge. A neutral
// steel-blue so it never clashes with the per-agent program badge colors or
// the repo palette: it is meta-information (where), semantically distinct from
// the agent (what) and the repo (which).
const envBadgeColor = "#6E7B8B"

// envBadge renders a colored [host] pill for the execution environment of an
// instance, mirroring the program/repo badges. The local host is implicit (it
// is the machine running cs2) and renders nothing, so a local-only list stays
// uncluttered — exactly like the repo badge is hidden in single-repo lists.
// Only a remote (ssh) host gets a badge, because that is the only case where
// "where" is not implied. The background is taken from the surrounding line
// style so the badge blends on the selected (highlighted) row.
func envBadge(hostName string, bg lipgloss.TerminalColor) string {
	if hostName == "" || hostName == host.LocalAlias {
		return ""
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(envBadgeColor)).
		Background(bg).
		Render(fmt.Sprintf("[%s]", hostName))
}

// repoBadgePalette is a set of distinct colors assigned to repo names. A
// stable hash of the name picks the index, so in a multi-repo list the same
// repo always wears the same color across instances and renders, while
// different repos get different colors — letting you tell at a glance which
// instance works on which repo. Mirrors the program badge's per-agent color.
var repoBadgePalette = []string{
	"#9B59B6", // purple
	"#E67E22", // orange
	"#1ABC9C", // teal
	"#3498DB", // blue
	"#E91E63", // pink
	"#27AE60", // green
	"#F1C40F", // yellow
	"#E74C3C", // red
}

// repoBadgeColor picks a stable color from repoBadgePalette for the given repo
// name. Empty name falls back to the first color.
func repoBadgeColor(repoName string) string {
	if repoName == "" {
		return repoBadgePalette[0]
	}
	sum := 0
	for _, r := range repoName {
		sum += int(r)
	}
	return repoBadgePalette[sum%len(repoBadgePalette)]
}

// repoBadge renders a colored [repo-name] pill for the repository an instance
// works on, mirroring the [pi]/[claude] program badge. The background is taken
// from the surrounding line style so the badge blends on the selected
// (highlighted) row, just like the diff stats do.
func repoBadge(repoName string, bg lipgloss.TerminalColor) string {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(repoBadgeColor(repoName))).
		Background(bg).
		Render(fmt.Sprintf("[%s]", repoName))
}

type List struct {
	items         []*session.Instance
	selectedIdx   int
	height, width int
	renderer      *InstanceRenderer
	autoyes       bool

	// map of repo name to number of instances using it. Used to display the repo name only if there are
	// multiple repos in play.
	repos map[string]int
}

func NewList(spinner *spinner.Model, autoYes bool) *List {
	return &List{
		items:    []*session.Instance{},
		renderer: &InstanceRenderer{spinner: spinner},
		repos:    make(map[string]int),
		autoyes:  autoYes,
	}
}

// SetSize sets the height and width of the list.
func (l *List) SetSize(width, height int) {
	l.width = width
	l.height = height
	l.renderer.setWidth(width)
}

// SetSessionPreviewSize sets the height and width for the tmux sessions. This makes the stdout line have the correct
// width and height.
func (l *List) SetSessionPreviewSize(width, height int) (err error) {
	for i, item := range l.items {
		if !item.Started() || item.Paused() {
			continue
		}

		if innerErr := item.SetPreviewSize(width, height); innerErr != nil {
			err = errors.Join(
				err, fmt.Errorf("could not set preview size for instance %d: %v", i, innerErr))
		}
	}
	return
}

func (l *List) NumInstances() int {
	return len(l.items)
}

// InstanceRenderer handles rendering of session.Instance objects
type InstanceRenderer struct {
	spinner *spinner.Model
	width   int
}

func (r *InstanceRenderer) setWidth(width int) {
	r.width = AdjustPreviewWidth(width)
}

// ɹ and ɻ are other options.
const branchIcon = "Ꮧ"

func (r *InstanceRenderer) Render(i *session.Instance, idx int, selected bool, hasMultipleRepos bool) string {
	prefix := fmt.Sprintf(" %d. ", idx)
	if idx >= 10 {
		prefix = prefix[:len(prefix)-1]
	}
	titleS := selectedTitleStyle
	descS := selectedDescStyle
	if !selected {
		titleS = titleStyle
		descS = listDescStyle
	}

	// add spinner next to title if it's running
	var join string
	switch i.Status {
	case session.Running, session.Loading:
		join = fmt.Sprintf("%s ", r.spinner.View())
	case session.Ready:
		join = readyStyle.Render(readyIcon)
	case session.Paused:
		join = pausedStyle.Render(pausedIcon)
	default:
	}

	// Cut the title if it's too long
	titleText := i.Title
	widthAvail := r.width - 3 - runewidth.StringWidth(prefix) - 1
	if widthAvail > 0 && runewidth.StringWidth(titleText) > widthAvail {
		titleText = runewidth.Truncate(titleText, widthAvail-3, "...")
	}
	title := titleS.Render(lipgloss.JoinHorizontal(
		lipgloss.Left,
		lipgloss.Place(r.width-3, 1, lipgloss.Left, lipgloss.Center, fmt.Sprintf("%s %s", prefix, titleText)),
		" ",
		join,
		" ",
		programBadge(i.Program),
	))

	stat := i.GetDiffStats()

	var diff string
	var addedDiff, removedDiff string
	if stat == nil || stat.Error != nil || stat.IsEmpty() {
		// Don't show diff stats if there's an error or if they don't exist
		addedDiff = ""
		removedDiff = ""
		diff = ""
	} else {
		addedDiff = fmt.Sprintf("+%d", stat.Added)
		removedDiff = fmt.Sprintf("-%d ", stat.Removed)
		diff = lipgloss.JoinHorizontal(
			lipgloss.Center,
			addedLinesStyle.Background(descS.GetBackground()).Render(addedDiff),
			lipgloss.Style{}.Background(descS.GetBackground()).Foreground(descS.GetForeground()).Render(","),
			removedLinesStyle.Background(descS.GetBackground()).Render(removedDiff),
		)
	}

	remainingWidth := r.width
	remainingWidth -= runewidth.StringWidth(prefix)
	remainingWidth -= runewidth.StringWidth(branchIcon)
	remainingWidth -= 2 // for the literal " " and "-" in the branchLine format string

	diffWidth := runewidth.StringWidth(addedDiff) + runewidth.StringWidth(removedDiff)
	if diffWidth > 0 {
		diffWidth += 1
	}

	// Use fixed width for diff stats to avoid layout issues
	remainingWidth -= diffWidth

	branch := i.Branch

	// Repo badge: a colored [repo-name] pill mirroring the [pi]/[claude]
	// program badge. Only shown once the instance is started (the repo name
	// is read from the worktree) and when more than one repo is in play, so a
	// single-repo list stays uncluttered (the repo is implied). It sits next
	// to the branch because both describe where the work happens.
	badge := ""
	if i.Started() && hasMultipleRepos {
		if repoName, err := i.RepoName(); err != nil {
			log.ErrorLog.Printf("could not get repo name in instance renderer: %v", err)
		} else {
			// "[" + name + "]" + the leading separator space.
			badgeWidth := runewidth.StringWidth(repoName) + 3
			if remainingWidth >= badgeWidth {
				badge = " " + repoBadge(repoName, descS.GetBackground())
				remainingWidth -= badgeWidth
			}
		}
	}

	// Environment (host) badge: a [host] pill for remote (ssh) instances only.
	// Local is implicit (the machine running cs2) and renders nothing, so a
	// local-only list stays uncluttered. Unlike the repo badge this is shown in
	// both single- and multi-repo lists: "where" is never implied by the repo,
	// and the env badge is most useful precisely when one repo runs on several
	// hosts. Sits after the repo badge, before the diff stats.
	hostName := i.Host().Name()
	if eb := envBadge(hostName, descS.GetBackground()); eb != "" {
		envWidth := runewidth.StringWidth(eb) + 1 // +1 for the leading separator space
		if remainingWidth >= envWidth {
			badge += " " + eb
			remainingWidth -= envWidth
		}
	}

	// Don't show branch if there's no space for it. Or show ellipsis if it's too long.
	branchWidth := runewidth.StringWidth(branch)
	if remainingWidth < 0 {
		branch = ""
	} else if remainingWidth < branchWidth {
		if remainingWidth < 3 {
			branch = ""
		} else {
			// We know the remainingWidth is at least 4 and branch is longer than that, so this is safe.
			branch = runewidth.Truncate(branch, remainingWidth-3, "...")
		}
	}
	remainingWidth -= runewidth.StringWidth(branch)

	// Add spaces to fill the remaining width.
	spaces := ""
	if remainingWidth > 0 {
		spaces = strings.Repeat(" ", remainingWidth)
	}

	branchLine := fmt.Sprintf("%s %s-%s%s%s%s", strings.Repeat(" ", len(prefix)), branchIcon, branch, badge, spaces, diff)

	// join title and subtitle
	text := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		descS.Render(branchLine),
	)

	return text
}

func (l *List) String() string {
	const titleText = " Instances "
	const autoYesText = " auto-yes "

	// Write the title.
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString("\n")

	// Write title line
	// add padding of 2 because the border on list items adds some extra characters
	titleWidth := AdjustPreviewWidth(l.width) + 2
	if !l.autoyes {
		b.WriteString(lipgloss.Place(
			titleWidth, 1, lipgloss.Left, lipgloss.Bottom, mainTitle.Render(titleText)))
	} else {
		title := lipgloss.Place(
			titleWidth/2, 1, lipgloss.Left, lipgloss.Bottom, mainTitle.Render(titleText))
		autoYes := lipgloss.Place(
			titleWidth-(titleWidth/2), 1, lipgloss.Right, lipgloss.Bottom, autoYesStyle.Render(autoYesText))
		b.WriteString(lipgloss.JoinHorizontal(
			lipgloss.Top, title, autoYes))
	}

	b.WriteString("\n")
	b.WriteString("\n")

	// Render the list.
	for i, item := range l.items {
		b.WriteString(l.renderer.Render(item, i+1, i == l.selectedIdx, len(l.repos) > 1))
		if i != len(l.items)-1 {
			b.WriteString("\n\n")
		}
	}
	return lipgloss.Place(l.width, l.height, lipgloss.Left, lipgloss.Top, b.String())
}

// Down selects the next item in the list.
func (l *List) Down() {
	if len(l.items) == 0 {
		return
	}
	if l.selectedIdx < len(l.items)-1 {
		l.selectedIdx++
	} else {
		l.selectedIdx = 0
	}
}

// Kill selects the next item in the list.
func (l *List) Kill() {
	if len(l.items) == 0 {
		return
	}
	targetInstance := l.items[l.selectedIdx]

	// Kill the tmux session
	if err := targetInstance.Kill(); err != nil {
		log.ErrorLog.Printf("could not kill instance: %v", err)
	}

	// If you delete the last one in the list, select the previous one.
	if l.selectedIdx == len(l.items)-1 {
		defer l.Up()
	}

	// Unregister the reponame.
	repoName, err := targetInstance.RepoName()
	if err != nil {
		log.ErrorLog.Printf("could not get repo name: %v", err)
	} else {
		l.rmRepo(repoName)
	}

	// Since there's items after this, the selectedIdx can stay the same.
	l.items = append(l.items[:l.selectedIdx], l.items[l.selectedIdx+1:]...)
}

func (l *List) Attach() (chan struct{}, error) {
	targetInstance := l.items[l.selectedIdx]
	return targetInstance.Attach()
}

// Up selects the prev item in the list.
func (l *List) Up() {
	if len(l.items) == 0 {
		return
	}
	if l.selectedIdx > 0 {
		l.selectedIdx--
	} else {
		l.selectedIdx = len(l.items) - 1
	}
}

func (l *List) addRepo(repo string) {
	if _, ok := l.repos[repo]; !ok {
		l.repos[repo] = 0
	}
	l.repos[repo]++
}

func (l *List) rmRepo(repo string) {
	if _, ok := l.repos[repo]; !ok {
		log.ErrorLog.Printf("repo %s not found", repo)
		return
	}
	l.repos[repo]--
	if l.repos[repo] == 0 {
		delete(l.repos, repo)
	}
}

// AddInstance adds a new instance to the list. It returns a finalizer function that should be called when the instance
// is started. If the instance was restored from storage or is paused, you can call the finalizer immediately.
// When creating a new one and entering the name, you want to call the finalizer once the name is done.
func (l *List) AddInstance(instance *session.Instance) (finalize func()) {
	l.items = append(l.items, instance)
	// The finalizer registers the repo name once the instance is started.
	return func() {
		repoName, err := instance.RepoName()
		if err != nil {
			log.ErrorLog.Printf("could not get repo name: %v", err)
			return
		}

		l.addRepo(repoName)
	}
}

// GetSelectedInstance returns the currently selected instance
func (l *List) GetSelectedInstance() *session.Instance {
	if len(l.items) == 0 {
		return nil
	}
	return l.items[l.selectedIdx]
}

// SetSelectedInstance sets the selected index. Noop if the index is out of bounds.
func (l *List) SetSelectedInstance(idx int) {
	if idx >= len(l.items) {
		return
	}
	l.selectedIdx = idx
}

// SelectInstance finds and selects the given instance in the list.
func (l *List) SelectInstance(target *session.Instance) {
	for i, inst := range l.items {
		if inst == target {
			l.SetSelectedInstance(i)
			return
		}
	}
}

// MoveUp swaps the selected instance with the one above it.
func (l *List) MoveUp() bool {
	if l.selectedIdx <= 0 || len(l.items) < 2 {
		return false
	}
	l.items[l.selectedIdx], l.items[l.selectedIdx-1] = l.items[l.selectedIdx-1], l.items[l.selectedIdx]
	l.selectedIdx--
	return true
}

// MoveDown swaps the selected instance with the one below it.
func (l *List) MoveDown() bool {
	if l.selectedIdx >= len(l.items)-1 || len(l.items) < 2 {
		return false
	}
	l.items[l.selectedIdx], l.items[l.selectedIdx+1] = l.items[l.selectedIdx+1], l.items[l.selectedIdx]
	l.selectedIdx++
	return true
}

// GetInstances returns all instances in the list
func (l *List) GetInstances() []*session.Instance {
	return l.items
}

// SetInstances replaces the list's items wholesale, re-derives the repo
// counts, and preserves the selection by ID. It is the TUI's read-only
// reconcile path (C3.2): the TUI keeps a read-only cache of the fleet
// refreshed from the kernel's list_instances_full snapshot, and this method
// applies a new snapshot to the view without losing the user's selection.
//
// The caller owns the reconstruction of the *session.Instance view handles
// (via session.FromInstanceData); the list only owns the view ordering and
// per-instance bookkeeping (repo counts, selection).
func (l *List) SetInstances(items []*session.Instance) {
	// Remember the selected instance's ID so we can restore it after replace.
	var selectedID string
	if len(l.items) > 0 && l.selectedIdx < len(l.items) {
		selectedID = l.items[l.selectedIdx].GetID()
	}

	l.items = items

	// Re-derive repo counts from the new set.
	l.repos = make(map[string]int, len(items))
	for _, inst := range items {
		if inst.Started() {
			if repoName, err := inst.RepoName(); err == nil && repoName != "" {
				l.repos[repoName]++
			}
		}
	}

	// Restore selection by ID; fall back to 0 (or no selection) if absent.
	l.selectedIdx = 0
	if selectedID != "" {
		for i, inst := range items {
			if inst.GetID() == selectedID {
				l.selectedIdx = i
				break
			}
		}
	}
	if len(items) == 0 {
		l.selectedIdx = 0
	}
}

// FindInstance returns the instance with the given ID, or nil if absent.
func (l *List) FindInstance(id string) *session.Instance {
	for _, inst := range l.items {
		if inst.GetID() == id {
			return inst
		}
	}
	return nil
}
