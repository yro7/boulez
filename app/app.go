package app

import (
	"context"
	"fmt"
	"github.com/yro7/boulez/config"
	"github.com/yro7/boulez/host"
	"github.com/yro7/boulez/keys"
	"github.com/yro7/boulez/log"
	"github.com/yro7/boulez/orchestrator"
	"github.com/yro7/boulez/prefs"
	"github.com/yro7/boulez/presets"
	"github.com/yro7/boulez/program"
	"github.com/yro7/boulez/repo"
	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/session/git"
	"github.com/yro7/boulez/ui"
	"github.com/yro7/boulez/ui/overlay"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// Run is the main entrypoint into the application.
func Run(ctx context.Context, program string, autoYes bool) error {
	p := tea.NewProgram(
		newHome(ctx, program, autoYes),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(), // Mouse scroll
	)
	_, err := p.Run()
	return err
}

type state int

const (
	stateDefault state = iota
	// stateNew is the state when the user is creating a new instance.
	stateNew
	// statePrompt is the state when the user is entering a prompt.
	statePrompt
	// stateHelp is the state when a help screen is displayed.
	stateHelp
	// stateConfirm is the state when a confirmation modal is displayed.
	stateConfirm
	// stateRepoSelect is the state when the user is choosing a repository for a
	// new instance (registry + free path). It runs before instance creation.
	stateRepoSelect
	// stateHostSelect is the state when the user is choosing an execution host
	// (local or a known ssh alias) for a new instance. It runs before repo
	// selection, giving the flow: host → repo → branch.
	stateHostSelect
	// statePresetSelect is the state when the user is choosing a named preset
	// (Ctrl+R) to start a quick session. On submit the host/repo/prompt
	// selectors are skipped entirely: only the instance name remains to type.
	statePresetSelect
	// stateInsert is the vim-style insert mode: keystrokes are forwarded
	// directly to the selected instance's tmux pane (via forwardInsertKey /
	// Instance.SendKey) instead of being interpreted as fleet keybindings.
	// Entered from stateDefault with `i` (only on the Preview tab, with a
	// started, non-paused instance), exited with Esc. The modal separation is
	// what stops fleet bindings (q, c, r, p, ...) from colliding with text the
	// user types into the agent. There is NO local text buffer — each key is
	// injected as-is, so the agent's own readline/editor (backspace, history,
	// completion, multi-line, IME) is the authority. The PreviewPane only
	// renders a `-- INSERT --` banner.
	stateInsert
)

type home struct {
	ctx context.Context

	// -- Storage and Configuration --

	agentProgram string // the default agent binary name (e.g. "claude"), from the --program flag
	autoYes      bool

	// appConfig stores persistent application configuration
	appConfig *config.Config
	// appState stores persistent application state like seen help screens
	appState config.AppState

	// -- State --

	// state is the current discrete state of the application
	state state
	// newInstanceFinalizer is called when the state is stateNew and then you press enter.
	// It registers the new instance in the list after the instance has been started.
	newInstanceFinalizer func()

	// promptAfterName tracks if we should enter prompt mode after naming
	promptAfterName bool

	// keySent is used to manage underlining menu items
	keySent bool

	// instanceStarting is true while a background spawn syscall is in
	// flight (C3.3). Prevents double-submission of the O / n / Shift+N keys.
	// The draft instance is held in the list (Loading) and removed on the
	// spawn ack.
	instanceStarting bool
	// pendingDraftIDs tracks instance IDs that the TUI has added to the
	// list as local drafts (during name entry or while a spawn syscall is in
	// flight) but that the kernel does not yet know about. reconcileFleet
	// preserves these against the read-only fleet snapshot so the periodic
	// fleet tick (C3.2) does not wipe a draft mid-name-entry (which would
	// panic the stateNew handler with index out of range). An ID is removed
	// here when the draft leaves the list: on spawn ack (success or error)
	// or when the user cancels name entry.
	pendingDraftIDs map[string]struct{}

	// -- UI Components --

	// list displays the list of instances
	list *ui.List
	// menu displays the bottom menu
	menu *ui.Menu
	// tabbedWindow displays the tabbed window with preview and diff panes
	tabbedWindow *ui.TabbedWindow
	// errBox displays error messages
	errBox *ui.ErrBox
	// global spinner instance. we plumb this down to where it's needed
	spinner spinner.Model
	// textInputOverlay handles text input with state
	textInputOverlay *overlay.TextInputOverlay
	// textOverlay displays text information
	textOverlay *overlay.TextOverlay
	// confirmationOverlay displays confirmation modals
	confirmationOverlay *overlay.ConfirmationOverlay
	// repoSelector displays the repo picker at instance creation
	repoSelector *overlay.RepoSelector
	// hostSelector displays the host picker at instance creation (before the
	// repo picker). The chosen host is stored in pendingHost and applied to
	// the instance in startNewInstance.
	hostSelector *overlay.HostSelector
	// pendingHost is the host chosen in the host selector, carried into the
	// repo selector and finally into the instance.
	pendingHost host.Host

	// repoRegistry is the persistent set of known repository paths, used to
	// pre-populate the repo selector. Free paths chosen at creation are added
	// back here so they reappear next time.
	repoRegistry *repo.Registry
	// hostRegistry is the persistent set of known ssh aliases, used to
	// pre-populate the host selector. Free aliases chosen at creation are added
	// back here so they reappear next time.
	hostRegistry *host.Registry

	// prefs is the persistent repo→profile preference store. At instance
	// creation, if a preference exists for the selected repo, the matching
	// profile is preselected in the prompt overlay. Set explicitly via ctrl+s
	// on the profile picker (see handlePromptState).
	prefs *prefs.Store

	// presetStore is the persistent named-preset store for quick sessions
	// (Ctrl+R). Read fresh on every open so an agent or editor can change
	// ~/.boulez/presets.json between two opens with no watcher.
	presetStore *presets.Store
	// presetSelector displays the named-preset picker at instance creation
	// (Ctrl+R). On submit the chosen preset's host/repo/profile/branch/prompt
	// are applied directly and the flow jumps to name entry, skipping the
	// host/repo/prompt overlays.
	presetSelector *overlay.PresetSelector

	// repoSelectPrompt tracks whether the repo selector was opened from the
	// prompt (KeyPrompt) flow; if so, after the repo is chosen we continue
	// straight into the prompt+branch overlay.
	repoSelectPrompt bool

	// landCaller performs the kernel Land syscall for the L-key action. The
	// default is the socket-backed adapter (the daemon owns the kernel); tests
	// inject a fake. Nil defaults to newSocketLandCaller() at first use so a
	// bare &home{} test construct still works.
	landCaller session.LandCaller

	// landInFlight tracks instance IDs with a land Cmd currently running. It
	// backs the anti-double-land guard (a second L press while a land is in
	// flight is refused with a message instead of a silent no-op) and is
	// cleared on landDoneMsg (success, conflict, or error). Per-instance, not
	// global, so landin two different instances concurrently is allowed.
	landInFlight map[string]struct{}

	// workingStreak is the per-instance hysteresis counter for the
	// Ready→Running transition. A single "pane changed" tick is not enough to
	// demote a Ready instance: the pane chrome (Pi's animated spinner, the
	// context-usage percentage, the cursor) keeps hashing differently even
	// when the agent is idle, so without hysteresis an idle instance flickers
	// Ready↔Running every 500ms tick. The counter accumulates consecutive
	// "really working" ticks (pane changed AND no authoritative Ready signal)
	// and only flips Ready→Running once it reaches readyToWorkingTicks. A Ready
	// signal (adapter StatusReady) or a stable pane resets it. Pure TUI view
	// state — not persisted, not on the wire. Keyed by instance ID; pruned in
	// reconcileFleet to IDs no longer in the kernel's snapshot.
	workingStreak map[string]int

	// previewCaptureInFlight is the single-flight guard for the async preview
	// capture (see instanceChanged). The preview pane captures the instance's
	// own tmux pane, which for a remote (ssh) instance is a network round-trip;
	// it is fetched in a goroutine and applied via previewContentMsg so it never
	// blocks the Bubble Tea update thread. previewTickMsg re-arms every 100ms
	// regardless of whether the previous capture returned, so without this guard
	// a capture slower than the tick (the remote case) would pile up overlapping
	// goroutines. Set true when a capture is dispatched, cleared on
	// previewContentMsg.
	previewCaptureInFlight bool

	// previewErrEvery throttles the log line for a failed preview capture. A
	// remote instance whose host is down fails every ~100ms tick; without a
	// throttle the log would fill with one identical line per tick. The error
	// is already surfaced in the preview pane (SetPreviewError), so the log is
	// only for post-mortem debugging — every 30s is plenty.
	previewErrEvery *log.Every

	// fleet is the TUI's seam over the daemon's control socket (C3.1). The
	// TUI is a pure client of the kernel: it owns the VIEW (a read-only cache
	// of the fleet), not the TRUTH. Every fleet mutation goes through this
	// seam; the daemon's kernel is the single writer. Nil defaults to
	// newSocketFleetClient() at first use so test homes can inject a fake.
	fleet fleetClient
}

func newHome(ctx context.Context, program string, autoYes bool) *home {
	// Load application config
	appConfig := config.LoadConfig()

	// Load application state
	appState := config.LoadState()

	// The registry is best-effort: if it cannot be opened, the selector still
	// works with a free path, so a nil registry is tolerated at the call sites.
	repoRegistry, _ := repo.NewRegistry()
	hostRegistry, _ := host.NewRegistry()
	prefStore, _ := prefs.NewStore()
	presetStore, _ := presets.NewStore()

	h := &home{
		ctx:             ctx,
		spinner:         spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		menu:            ui.NewMenu(),
		tabbedWindow:    ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane()),
		errBox:          ui.NewErrBox(),
		appConfig:       appConfig,
		repoRegistry:    repoRegistry,
		hostRegistry:    hostRegistry,
		prefs:           prefStore,
		presetStore:     presetStore,
		agentProgram:   program,
		autoYes:         autoYes,
		state:           stateDefault,
		appState:        appState,
		pendingDraftIDs: make(map[string]struct{}),
		landInFlight:    make(map[string]struct{}),
		workingStreak:   make(map[string]int),
		previewErrEvery: log.NewEvery(30 * time.Second),
	}
	h.list = ui.NewList(&h.spinner, autoYes)

	// The TUI is a pure client of the kernel (C3.2): the fleet is loaded
	// from the daemon's control socket, not from local storage. The kernel
	// is the single writer; the TUI keeps a read-only cache reconciled on
	// the poll cadence (see fleetTickMsg) and after every mutation ack.
	//
	// Failure to read the fleet at boot is fatal: the TUI is a viewer of the
	// kernel, no kernel, no viewer (decision D2). main.go's
	// ensureDaemonRunning has already brought the daemon up; a failure here
	// means the socket is unreachable despite that, which is a hard error.
	if err := h.refreshFleetFromKernel(); err != nil {
		fmt.Printf("Failed to read fleet from daemon: %v\n", err)
		os.Exit(1)
	}

	return h
}

// updateHandleWindowSizeEvent sets the sizes of the components.
// The components will try to render inside their bounds.
func (m *home) updateHandleWindowSizeEvent(msg tea.WindowSizeMsg) {
	// List takes 30% of width, preview takes 70%
	listWidth := int(float32(msg.Width) * 0.3)
	tabsWidth := msg.Width - listWidth

	// Menu takes 10% of height, list and window take 90%
	contentHeight := int(float32(msg.Height) * 0.9)
	menuHeight := msg.Height - contentHeight - 1     // minus 1 for error box
	m.errBox.SetSize(int(float32(msg.Width)*0.9), 1) // error box takes 1 row

	m.tabbedWindow.SetSize(tabsWidth, contentHeight)
	m.list.SetSize(listWidth, contentHeight)

	if m.textInputOverlay != nil {
		m.textInputOverlay.SetSize(int(float32(msg.Width)*0.6), int(float32(msg.Height)*0.4))
	}
	if m.textOverlay != nil {
		m.textOverlay.SetWidth(int(float32(msg.Width) * 0.6))
	}
	if m.repoSelector != nil {
		m.repoSelector.SetWidth(int(float32(msg.Width) * 0.6))
	}
	if m.hostSelector != nil {
		m.hostSelector.SetWidth(int(float32(msg.Width) * 0.6))
	}
	if m.presetSelector != nil {
		m.presetSelector.SetWidth(int(float32(msg.Width) * 0.6))
	}

	previewWidth, previewHeight := m.tabbedWindow.GetPreviewSize()
	if err := m.list.SetSessionPreviewSize(previewWidth, previewHeight); err != nil {
		log.ErrorLog.Print(err)
	}
	m.menu.SetSize(msg.Width, menuHeight)
}

func (m *home) Init() tea.Cmd {
	// Upon starting, we want to start the spinner. Whenever we get a spinner.TickMsg, we
	// update the spinner, which sends a new spinner.TickMsg. I think this lasts forever lol.
	return tea.Batch(
		m.spinner.Tick,
		func() tea.Msg {
			time.Sleep(100 * time.Millisecond)
			return previewTickMsg{}
		},
		tickUpdateMetadataCmd(m.snapshotActiveInstances(), m.list.GetSelectedInstance()),
		// Fleet refresh cadence (C3.2): the TUI is a client of the kernel and
		// reconciles its read-only cache against list_instances_full on a steady
		// tick so external mutations become visible without per-keystroke
		// round-trips.
		func() tea.Msg {
			time.Sleep(fleetTickInterval)
			return fleetTickMsg{}
		},
	)
}

func (m *home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case hideErrMsg:
		m.errBox.Clear()
	case attachFinishedMsg:
		// A tea.ExecProcess attach (Preview tab) has returned: the Bubbletea
		// terminal has been restored. Reset state and refresh the preview so the
		// pane reflects whatever the agent produced while attached.
		m.state = stateDefault
		return m, tea.Sequence(
			tea.WindowSize(),
			m.instanceChanged(),
		)
	case previewTickMsg:
		cmd := m.instanceChanged()
		return m, tea.Batch(
			cmd,
			func() tea.Msg {
				time.Sleep(100 * time.Millisecond)
				return previewTickMsg{}
			},
		)
	case previewContentMsg:
		// A preview capture dispatched by instanceChanged has returned. Clear
		// the single-flight guard first (always, even on error or a stale
		// result) so the next tick can dispatch again.
		m.previewCaptureInFlight = false
		selected := m.list.GetSelectedInstance()
		if msg.err != nil {
			// Surface the capture error IN the pane rather than the error
			// box: a failed `ssh <alias> tmux capture-pane` is a per-tick
			// event for a downed remote host, so handleError's 3s error box
			// would both flash every tick and spam the log. SetPreviewError
			// replaces any stale "Setting up workspace..." fallback left
			// over from the Loading boot phase with a connectivity message,
			// so a Running-but-unreachable instance no longer looks stuck
			// booting. The capture is retried by the next previewTick (the
			// single-flight guard is cleared above), so the pane self-heals
			// when the host comes back.
			if selected != nil && selected.GetID() == msg.instanceID {
				m.tabbedWindow.SetPreviewError(selected, msg.err)
			}
			if m.previewErrEvery.ShouldLog() {
				log.WarningLog.Printf("preview capture failed for %s: %v", msg.instanceID, msg.err)
			}
			return m, nil
		}
		// Drop the capture if the selection moved on while it was in flight —
		// painting it now would show one instance's pane under another's title.
		if selected != nil && selected.GetID() == msg.instanceID {
			m.tabbedWindow.SetPreviewContent(selected, msg.content)
		}
		return m, nil
	case keyupMsg:
		m.menu.ClearKeydown()
		return m, nil
	case fleetTickMsg:
		// Refresh the read-only fleet cache from the kernel. Errors are
		// surfaced non-fatally: a transient socket error must not kill the TUI
		// (the kernel is the authority; the view just stays briefly stale and
		// retries on the next tick).
		if err := m.refreshFleetFromKernel(); err != nil {
			return m, tea.Batch(
				m.handleError(err),
				func() tea.Msg {
					time.Sleep(fleetTickInterval)
					return fleetTickMsg{}
				},
			)
		}
		return m, tea.Batch(
			m.instanceChanged(),
			func() tea.Msg {
				time.Sleep(fleetTickInterval)
				return fleetTickMsg{}
			},
		)
	case metadataUpdateDoneMsg:
		for _, r := range msg.results {
			// Skip instances that were paused while metadata was being computed
			if r.instance.Status == session.Paused {
				continue
			}
			// The adapter's detected status is the source of truth and takes
			// priority over the content-change heuristic. Previously, a ready
			// sentinel landing in the pane was classified as "working" merely
			// because the pane content changed, leaving finished agents stuck on
			// the running spinner forever.
			prevStatus := r.instance.Status
			if m.workingStreak == nil {
				m.workingStreak = make(map[string]int)
			}
			switch {
			case r.status == program.StatusReady:
				// An authoritative Ready signal (e.g. the Pi sentinel) resets the
				// working hysteresis: the next working phase starts counting from 1.
				delete(m.workingStreak, r.instance.GetID())
				r.instance.SetStatus(session.Ready)
			case r.status == program.StatusPermission:
				// A resolvable permission/trust prompt. Only auto-resolve when
				// AutoYes is on, mirroring the original TapEnter() gating:
				// the user explicitly turned off auto-yes, so do not dismiss
				// prompts for them. The instance status is left unchanged
				// (Running) since the agent is waiting for a permission
				// decision, not free input.
				if r.instance.AutoYes {
					r.instance.CheckAndHandleTrustPrompt()
				}
			default:
				// StatusWorking (or StatusUnknown for agents we don't detect):
				// an authoritative adapter signal always wins. When the adapter is
				// silent, fall back to pane-content stability: if the pane hasn't
				// changed for longer than stableReadyThreshold, the agent is
				// presumed idle (waiting on input or a permission, not streaming).
				// This is the agent-agnostic net — boulez stays usable for any
				// harness without a dedicated adapter. A transient false Ready
				// (long silent tool run) self-corrects as soon as output resumes.
				if r.updated {
					// Hysteresis on Ready→Running: a single "pane changed" tick is
					// not enough to demote a Ready instance. The pane chrome (Pi's
					// animated spinner, the context-usage percentage, the cursor)
					// keeps hashing differently even when the agent is idle, so
					// without this gate an idle instance flickers Ready↔Running
					// every 500ms tick. Require `readyToWorkingTicks` consecutive
					// working ticks before flipping. A Running instance stays
					// Running on a single tick (no gate needed there); a Ready
					// signal or a stable pane resets the streak.
					if prevStatus == session.Ready {
						m.workingStreak[r.instance.GetID()]++
						if m.workingStreak[r.instance.GetID()] < readyToWorkingTicks {
							// Hold Ready: not enough consecutive working ticks yet.
							r.instance.SetStatus(session.Ready)
						} else {
							delete(m.workingStreak, r.instance.GetID())
							r.instance.SetStatus(session.Running)
						}
					} else {
						// Running (or other) → stay Running; no streak accumulated.
						delete(m.workingStreak, r.instance.GetID())
						r.instance.SetStatus(session.Running)
					}
				} else if r.stableFor >= stableReadyThreshold {
					// Stable pane: idle. Resets the working streak.
					delete(m.workingStreak, r.instance.GetID())
					r.instance.SetStatus(session.Ready)
				} else if prevStatus != session.Ready {
					// Running and no change: keep Running, keep the streak clear.
					delete(m.workingStreak, r.instance.GetID())
				}
				// If prevStatus == Ready, r.updated == false, stableFor < threshold:
				// the pane hasn't changed but not long enough to be idle. Hold
				// Ready (no SetStatus call) and reset the streak — a stable tick
				// means the agent isn't actively working, so the next working
				// phase starts counting from 1.
				if prevStatus == session.Ready && !r.updated && r.stableFor < stableReadyThreshold {
					delete(m.workingStreak, r.instance.GetID())
				}
			}
			// Notify on the Ready transition when configured.
			if prevStatus != session.Ready && r.instance.Status == session.Ready {
				// The agent finished a turn after having resumed work (Running →
				// Ready): the displayed version no longer matches what was last
				// landed, so clear the TUI-only landed hint. The dimmed row +
				// checkmark disappears, signalling the work has moved past the
				// merged snapshot.
				r.instance.SetLanded(false)
				if m.appConfig.NotifyOnReady {
					m.notifyReady(r.instance)
				}
			}
			if r.diffStats != nil && r.diffStats.Error != nil {
				if !strings.Contains(r.diffStats.Error.Error(), "base commit SHA not set") {
					log.WarningLog.Printf("could not update diff stats: %v", r.diffStats.Error)
				}
				r.instance.SetDiffStats(nil)
			} else {
				r.instance.SetDiffStats(r.diffStats)
			}
		}
		return m, tickUpdateMetadataCmd(m.snapshotActiveInstances(), m.list.GetSelectedInstance())
	case tea.MouseMsg:
		// Handle mouse wheel events for scrolling the diff/preview pane
		if msg.Action == tea.MouseActionPress {
			if msg.Button == tea.MouseButtonWheelDown || msg.Button == tea.MouseButtonWheelUp {
				selected := m.list.GetSelectedInstance()
				if selected == nil || selected.Status == session.Paused {
					return m, nil
				}

				switch msg.Button {
				case tea.MouseButtonWheelUp:
					m.tabbedWindow.ScrollUp()
				case tea.MouseButtonWheelDown:
					m.tabbedWindow.ScrollDown()
				}
			}
		}
		return m, nil
	case branchSearchDebounceMsg:
		// Debounce timer fired — check if this is still the current filter version
		if m.textInputOverlay == nil {
			return m, nil
		}
		if msg.version != m.textInputOverlay.BranchFilterVersion() {
			return m, nil // stale, a newer debounce is pending
		}
		return m, m.runBranchSearch(msg.repoPath, msg.filter, msg.version)
	case branchSearchResultMsg:
		if m.textInputOverlay != nil {
			m.textInputOverlay.SetBranchResults(msg.branches, msg.version)
		}
		return m, nil
	case reposFilteredMsg:
		// Late-arriving filter result: only apply if we're still in the repo
		// selector (user may have canceled). Narrowing in place keeps the
		// free-text input and cursor intact when possible.
		if m.state == stateRepoSelect && m.repoSelector != nil {
			m.repoSelector.SetRepos(msg.repos)
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKeyPress(msg)
	case tea.WindowSizeMsg:
		m.updateHandleWindowSizeEvent(msg)
		return m, nil
	case error:
		// Handle errors from confirmation actions
		return m, m.handleError(msg)
	case instanceChangedMsg:
		// Handle instance changed after confirmation action
		return m, m.instanceChanged()
	case landDoneMsg:
		return m, m.handleLandDone(msg)
	case spawnDoneMsg:
		// C3.3: spawn routed through the kernel. On success the kernel created
		// and started the instance; the TUI re-reads the fleet (C3.2) to pick it
		// up. The draft instance held in the list during name entry is removed
		// because the kernel owns the real instance now (with its own ID).
		if msg.draftID != "" {
			m.list.RemoveByID(msg.draftID)
			m.untrackDraft(msg.draftID)
		}
		if msg.err != nil {
			// No draft to clean beyond RemoveByID; the failed spawn just surfaces
			// the error. The view is already consistent (no kernel instance).
			return m, tea.Batch(m.handleError(msg.err), m.instanceChanged())
		}
		// Force a refresh so the new instance appears immediately, then run the
		// orchestrator post-spawn injection (if this was an O-key spawn).
		refresh := m.refreshFleetAfterMutation()
		if msg.orchestrator {
			return m, tea.Batch(refresh, m.injectOrchestratorContext(msg.id, msg.title))
		}
		// Select the freshly-spawned instance by title (the kernel allocated its
		// own ID, so we find it by the title we requested).
		m.selectInstanceByTitle(msg.title)
		return m, tea.Batch(tea.WindowSize(), m.instanceChanged(), refresh)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *home) handleQuit() (tea.Model, tea.Cmd) {
	// The kernel is the single writer (C3.5): it persists every mutation via
	// autosave, so the TUI has nothing to flush on quit.
	return m, tea.Quit
}

func (m *home) handleMenuHighlighting(msg tea.KeyMsg) (cmd tea.Cmd, returnEarly bool) {
	// Handle menu highlighting when you press a button. We intercept it here and immediately return to
	// update the ui while re-sending the keypress. Then, on the next call to this, we actually handle the keypress.
	if m.keySent {
		m.keySent = false
		return nil, false
	}
	if m.state == statePrompt || m.state == stateHelp || m.state == stateConfirm || m.state == stateRepoSelect || m.state == stateHostSelect || m.state == statePresetSelect || m.state == stateInsert {
		return nil, false
	}
	// If it's in the global keymap, we should try to highlight it.
	name, ok := keys.GlobalKeyStringsMap[msg.String()]
	if !ok {
		return nil, false
	}

	if m.list.GetSelectedInstance() != nil && m.list.GetSelectedInstance().Paused() && name == keys.KeyEnter {
		return nil, false
	}
	if name == keys.KeyShiftDown || name == keys.KeyShiftUp {
		return nil, false
	}

	// Skip the menu highlighting if the key is not in the map or we are using the shift up and down keys.
	// TODO: cleanup: when you press enter on stateNew, we use keys.KeySubmitName. We should unify the keymap.
	if name == keys.KeyEnter && m.state == stateNew {
		name = keys.KeySubmitName
	}
	m.keySent = true
	return tea.Batch(
		func() tea.Msg { return msg },
		m.keydownCallback(name)), true
}

func (m *home) handleKeyPress(msg tea.KeyMsg) (mod tea.Model, cmd tea.Cmd) {
	cmd, returnEarly := m.handleMenuHighlighting(msg)
	if returnEarly {
		return m, cmd
	}

	if m.state == stateHelp {
		return m.handleHelpState(msg)
	}

	if m.state == statePresetSelect {
		return m.handlePresetSelectState(msg)
	}

	if m.state == stateHostSelect {
		return m.handleHostSelectState(msg)
	}

	if m.state == stateRepoSelect {
		return m.handleRepoSelectState(msg)
	}

	if m.state == stateNew {
		return m.handleNewState(msg)
	} else if m.state == statePrompt {
		return m.handlePromptState(msg)
	} else if m.state == stateInsert {
		return m.handleInsertState(msg)
	}

	// Handle confirmation state
	if m.state == stateConfirm {
		closed, cmd := m.confirmationOverlay.HandleKeyPress(msg)
		if closed {
			m.state = stateDefault
			m.confirmationOverlay = nil
		}
		// Dispatch the action's Cmd (e.g. an async land/push/kill) so its
		// outcome reaches Update. Previously this was discarded, which is why
		// Land/Push/Kill gave zero feedback. The action's own returned msg
		// (e.g. instanceChangedMsg from kill) drives the view refresh; we do
		// not batch instanceChanged here so a minimal test home without a
		// tabbedWindow does not panic.
		return m, cmd
	}

	// Exit scrolling mode when ESC is pressed and preview pane is in scrolling mode
	// Check if Escape key was pressed and we're not in the diff tab (meaning we're in preview tab)
	// Always check for escape key first to ensure it doesn't get intercepted elsewhere
	if msg.Type == tea.KeyEsc {
		// Insert mode owns Esc as its exit key and must not be intercepted by
		// scroll-mode handling below; it is dispatched in handleInsertState.
		if m.state != stateInsert {
			// If in preview tab and in scroll mode, exit scroll mode
			if m.tabbedWindow.IsInPreviewTab() && m.tabbedWindow.IsPreviewInScrollMode() {
				// Use the selected instance from the list
				selected := m.list.GetSelectedInstance()
				err := m.tabbedWindow.ResetPreviewToNormalMode(selected)
				if err != nil {
					return m, m.handleError(err)
				}
				return m, m.instanceChanged()
			}
			// If in terminal tab and in scroll mode, exit scroll mode
			if m.tabbedWindow.IsInTerminalTab() && m.tabbedWindow.IsTerminalInScrollMode() {
				m.tabbedWindow.ResetTerminalToNormalMode()
				return m, m.instanceChanged()
			}
		}
	}

	// Handle quit commands first. Insert mode is excluded: it owns `q` as
	// a literal character (a user typing "quit" into the agent must not quit
	// the TUI). Esc is the dedicated exit from insert mode.
	if (msg.String() == "ctrl+c" || msg.String() == "q") && m.state != stateInsert {
		return m.handleQuit()
	}

	name, ok := keys.GlobalKeyStringsMap[msg.String()]
	if !ok {
		return m, nil
	}

	switch name {
	case keys.KeyHelp:
		return m.showHelpScreen(helpTypeGeneral{}, nil)
	case keys.KeyPrompt:
		return m, m.openHostSelector(true /* promptAfterName flow */)
	case keys.KeyNew:
		return m, m.openHostSelector(false /* plain new flow */)
	case keys.KeyQuickSession:
		return m, m.openPresetSelector()
	case keys.KeySpawnOrchestrator:
		return m, m.spawnOrchestrator()
	case keys.KeyInsert:
		return m.enterInsertMode()
	case keys.KeyUp:
		m.list.Up()
		return m, m.instanceChanged()
	case keys.KeyDown:
		m.list.Down()
		return m, m.instanceChanged()
	case keys.KeyShiftUp:
		m.tabbedWindow.ScrollUp()
		return m, m.instanceChanged()
	case keys.KeyShiftDown:
		m.tabbedWindow.ScrollDown()
		return m, m.instanceChanged()
	case keys.KeyTab:
		m.tabbedWindow.Toggle()
		m.menu.SetActiveTab(m.tabbedWindow.GetActiveTab())
		return m, m.instanceChanged()
	case keys.KeyKill:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading {
			return m, nil
		}

		// Create the kill action as a tea.Cmd
		killAction := func() tea.Msg {
			// Get worktree and check if branch is checked out
			worktree, err := selected.GetGitWorktree()
			if err != nil {
				return err
			}

			checkedOut, err := worktree.IsBranchCheckedOut()
			if err != nil {
				return err
			}

			if checkedOut {
				return fmt.Errorf("instance %s is currently checked out", selected.Title)
			}

			// Clean up terminal session for this instance
			m.tabbedWindow.CleanupTerminalForInstance(selected.Title)

			// Route kill through the kernel (C3.4): the kernel is the single
			// writer, so the kill (and the remove from the kernel's fleet) takes
			// effect on the authoritative copy. The TUI's view is reconciled by
			// the post-mutation fleet refresh, which surfaces the removal.
			if err := m.resolveFleet().Kill(selected.GetID()); err != nil {
				return err
			}
			// Re-read the fleet so the killed instance drops from the view.
			// Best-effort: a refresh failure only means the view is briefly
			// stale (the next fleet tick reconciles).
			_ = m.refreshFleetFromKernel()
			return instanceChangedMsg{}
		}

		// Show confirmation modal
		message := fmt.Sprintf("[!] Kill session '%s'?", selected.Title)
		return m, m.confirmAction(message, killAction)
	case keys.KeySubmit:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading {
			return m, nil
		}

		// Create the push action as a tea.Cmd
		pushAction := func() tea.Msg {
			// Default commit message with timestamp
			commitMsg := fmt.Sprintf("[boulez] update from '%s' on %s", selected.Title, time.Now().Format(time.RFC822))
			worktree, err := selected.GetGitWorktree()
			if err != nil {
				return err
			}
			if err = worktree.PushChanges(commitMsg, true); err != nil {
				return err
			}
			return nil
		}

		// Show confirmation modal
		message := fmt.Sprintf("[!] Push changes from session '%s'?", selected.Title)
		return m, m.confirmAction(message, pushAction)
	case keys.KeyLand:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading || selected.Status == session.Running {
			// Land is only offered for Ready/Paused (the menu hides it otherwise),
			// but defend in depth: never land an agent that is actively working.
			return m, nil
		}
		inst := selected
		targetBranch := "main"
		// Default commit message mirrors the push action's pattern so the two
		// gestures stay consistent.
		commitMsg := fmt.Sprintf("[boulez] update from '%s' on %s", inst.Title, time.Now().Format(time.RFC822))
		caller := m.landCaller
		if caller == nil {
			caller = newSocketLandCaller()
		}
		// Anti-double-land: if a land is already in flight for this instance,
		// refuse so the user gets feedback instead of a silent no-op (the kernel
		// would re-merge, but the TUI gave no indication either way before).
		if m.landInFlight == nil {
			m.landInFlight = make(map[string]struct{})
		}
		if _, busy := m.landInFlight[inst.GetID()]; busy {
			return m, m.handleError(fmt.Errorf("land already in progress for '%s'", inst.Title))
		}
		// Mark landing on the view handle so the renderer shows a spinner while
		// the (commit+push+merge) syscall runs in the background. The landInFlight
		// guard above ensures only one land runs per instance at a time.
		inst.SetLanding(true)
		message := fmt.Sprintf("[!] Land '%s' into '%s'?\n(commit + push '%s' then merge into %s)",
			inst.Title, targetBranch, inst.Title, targetBranch)
		return m, m.confirmAction(message, m.runLandCmd(inst, caller, targetBranch, commitMsg))
	case keys.KeyCheckout:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading || selected.Status == session.Dead {
			return m, nil
		}

		// Show help screen before pausing. The callback runs Pause as a side-
		// effect and returns a follow-up Cmd (instanceChanged + refresh); with
		// the help screen already seen it fires immediately, so we must propagate
		// its returned Cmd rather than discard it.
		mod, helpCmd := m.showHelpScreen(helpTypeInstanceCheckout{}, func() tea.Cmd {
			// Route pause through the kernel (C3.4): the kernel owns the tmux
			// session + worktree lifecycle, so the pause (and the persistence)
			// takes effect on the authoritative copy. The TUI's view is
			// reconciled by the post-mutation fleet refresh issued below.
			var cmds []tea.Cmd
			if err := m.resolveFleet().Pause(selected.GetID()); err != nil {
				cmds = append(cmds, m.handleError(err))
			}
			m.tabbedWindow.CleanupTerminalForInstance(selected.Title)
			if err := m.refreshFleetFromKernel(); err != nil {
				cmds = append(cmds, m.handleError(err))
			}
			cmds = append(cmds, m.instanceChanged())
			return tea.Batch(cmds...)
		})
		return mod, tea.Batch(helpCmd, m.instanceChanged(), m.refreshFleetAfterMutation())
	case keys.KeyMoveUp:
		// Reordering is view-only now (C3.5): the kernel's ordering is insertion
		// order and is not propagated back. Persisting a custom order across
		// restarts is a Phase 4 kernel-reconciliation concern.
		if m.list.MoveUp() {
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyMoveDown:
		if m.list.MoveDown() {
			return m, m.instanceChanged()
		}
		return m, nil
	case keys.KeyToggleAutoYes:
		selected := m.list.GetSelectedInstance()
		if selected == nil {
			return m, nil
		}
		// Toggle per-instance. NOTE (C3.5): there is no set-autoyes syscall yet,
		// so this toggle is view-only — the kernel's stored AutoYes overwrites
		// it on the next fleet refresh via reconcileFleet. Phase 4 adds a
		// set_autoyes syscall to make this authoritative.
		selected.SetAutoYes(!selected.AutoYes)
		return m, m.instanceChanged()
	case keys.KeyResume:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading || selected.Status == session.Dead {
			return m, nil
		}
		// Route resume through the kernel (C3.4): the kernel owns the tmux
		// session lifecycle, so the resume takes effect on the authoritative
		// copy. The TUI's view is reconciled by the post-mutation fleet refresh.
		if err := m.resolveFleet().Resume(selected.GetID()); err != nil {
			return m, m.handleError(err)
		}
		return m, tea.Batch(tea.WindowSize(), m.refreshFleetAfterMutation())
	case keys.KeyEnter:
		if m.list.NumInstances() == 0 {
			return m, nil
		}
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Paused() || selected.Status == session.Loading || !selected.TmuxAlive() {
			return m, nil
		}
		// Terminal tab: attach to terminal session. Commit 4 migrates this to
		// tea.ExecProcess too; for now the callback returns a tea.Cmd (required by
		// the new overlay contract) but the attach itself still runs the manual
		// PTY path via AttachTerminal. This is a local tmux session (fast,
		// non-SSH), so it does not exhibit the SSH freeze.
		if m.tabbedWindow.IsInTerminalTab() {
			mod, cmd := m.showHelpScreen(helpTypeInstanceAttach{}, func() tea.Cmd {
				ch, err := m.tabbedWindow.AttachTerminal()
				if err != nil {
					return m.handleError(err)
				}
				// Park the state reset on a goroutine so this callback returns
				// immediately (no blocking <-ch inside Update).
				go func() { <-ch; m.state = stateDefault }()
				return nil
			})
			return mod, cmd
		}
		// Preview tab: attach to the instance's tmux session via tea.ExecProcess,
		// which releases the Bubbletea terminal (alt-screen, raw mode, mouse) for
		// the command's duration and restores it on exit. The command is the
		// host's AttachCmd (local: tmux attach-session; ssh: ssh -t ... tmux
		// attach-session). This replaces the manual PTY + io.Copy + stdin
		// scavenger that froze the TUI over SSH (two readers on os.Stdin, no
		// terminal release). On exit, attachFinishedMsg resets state and refreshes
		// the preview.
		inst := selected
		mod, cmd := m.showHelpScreen(helpTypeInstanceAttach{}, func() tea.Cmd {
			return tea.ExecProcess(
				inst.Host().AttachCmd(inst.SessionName()),
				func(err error) tea.Msg {
					if err != nil {
						log.InfoLog.Printf("attach exited with error: %v", err)
					}
					return attachFinishedMsg{}
				},
			)
		})
		return mod, cmd
	default:
		return m, nil
	}
}

// enterInsertMode transitions from stateDefault to stateInsert, provided the
// selected instance can receive input (started, not paused, not loading) and
// the Preview tab is active. Insert mode is intentionally Preview-only for
// now: the Terminal tab already has its own attach flow, and duplicating the
// forwarding there is premature until a second site justifies the seam (per
// AGENTS.md: one adapter is a hypothetical seam, two make a real one).
func (m *home) enterInsertMode() (tea.Model, tea.Cmd) {
	if !m.tabbedWindow.IsInPreviewTab() {
		return m, nil
	}
	selected := m.list.GetSelectedInstance()
	if selected == nil || selected.Status == session.Loading || selected.Status == session.Paused || !selected.Started() {
		return m, nil
	}
	m.tabbedWindow.EnterInsertMode()
	m.state = stateInsert
	return m, nil
}

// handleInsertState routes each keystroke directly to the selected instance's
// tmux pane, keeping the dashboard visible. This is the "pure injection" path:
// there is no local text buffer — every key is forwarded as-is via
// forwardInsertKey, so the agent's own readline/editor (backspace, arrows,
// history, completion, multi-line, IME) is the authority over editing. The
// next previewTick (~10fps) re-captures the pane, so typed text is reflected
// in the preview without a forced refresh.
//
// Esc exits insert mode (it is intercepted here and never forwarded to the
// agent — same contract as vim's insert mode). Ctrl+C is forwarded as C-c so
// the user can interrupt a runaway agent; the global quit-on-ctrl+c guard is
// scoped to state != stateInsert so it reaches here.
func (m *home) handleInsertState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	selected := m.list.GetSelectedInstance()
	// Safety net: if the instance becomes unsendable mid-insert (killed,
	// paused, deselected), drop back to the default state rather than
	// forwarding keystrokes that can never be delivered.
	if selected == nil || selected.Status == session.Paused || !selected.Started() {
		m.tabbedWindow.ExitInsertMode()
		m.state = stateDefault
		return m, nil
	}
	if msg.String() == "esc" {
		m.tabbedWindow.ExitInsertMode()
		m.state = stateDefault
		return m, nil
	}
	if err := forwardInsertKey(selected, msg); err != nil {
		return m, m.handleError(err)
	}
	return m, nil
}

// instanceChanged updates the preview pane, menu, and diff pane based on the
// selected instance. The diff pane reads cached stats and the terminal pane
// runs a LOCAL tmux session (even for a remote instance), so both are cheap and
// stay synchronous. The preview pane, however, captures the instance's OWN tmux
// pane — for a remote (ssh) instance that is a network round-trip. Running it
// here on the Bubble Tea update thread froze the entire TUI (no keys, no
// redraw) until it returned, and re-froze on every 100ms preview tick. So the
// cheap preview state (fallback for nil/loading/paused, scroll-mode) is applied
// now and, when a live capture is actually needed, it is fetched in a goroutine
// and applied later via previewContentMsg. Returns a Cmd carrying that capture
// plus any error Cmd.
func (m *home) instanceChanged() tea.Cmd {
	// selected may be nil
	selected := m.list.GetSelectedInstance()

	m.tabbedWindow.UpdateDiff(selected)
	m.tabbedWindow.SetInstance(selected)
	// Update menu with current instance
	m.menu.SetInstance(selected)

	var cmds []tea.Cmd
	if err := m.tabbedWindow.UpdateTerminal(selected); err != nil {
		cmds = append(cmds, m.handleError(err))
	}

	// PreparePreview does the synchronous, non-blocking preview work and reports
	// whether a live pane capture is still needed. The single-flight guard keeps
	// a capture slower than the 100ms tick from spawning overlapping goroutines;
	// it is cleared when the previewContentMsg lands.
	if m.tabbedWindow.PreparePreview(selected) && !m.previewCaptureInFlight {
		m.previewCaptureInFlight = true
		id := selected.GetID()
		inst := selected
		cmds = append(cmds, func() tea.Msg {
			content, err := inst.Preview()
			return previewContentMsg{instanceID: id, content: content, err: err}
		})
	}

	return tea.Batch(cmds...)
}

// previewContentMsg carries a preview pane capture performed off the Bubble Tea
// update thread (see instanceChanged). instanceID pins the capture to the
// instance it was taken for, so a capture that only finishes after the
// selection changed is dropped rather than painted into the wrong pane.
type previewContentMsg struct {
	instanceID string
	content    string
	err        error
}

// attachFinishedMsg is delivered by tea.ExecProcess when an interactive attach
// (Preview tab) exits. The Bubbletea terminal has already been restored by the
// time this message is processed; the handler resets state and refreshes the
// preview.
type attachFinishedMsg struct{}

type keyupMsg struct{}

// keydownCallback clears the menu option highlighting after 500ms.
func (m *home) keydownCallback(name keys.KeyName) tea.Cmd {
	m.menu.Keydown(name)
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}

		return keyupMsg{}
	}
}

// hideErrMsg implements tea.Msg and clears the error text from the screen.
type hideErrMsg struct{}

// previewTickMsg implements tea.Msg and triggers a preview update
type previewTickMsg struct{}

type instanceChangedMsg struct{}

// branchSearchDebounceMsg fires after the debounce interval to trigger a search.
type branchSearchDebounceMsg struct {
	repoPath string
	filter   string
	version  uint64
}

// branchSearchResultMsg carries search results back to Update.
type branchSearchResultMsg struct {
	branches []string
	version  uint64
}

// reposFilteredMsg carries the host-filtered repo list back to Update. The
// repo selector is re-populated with only the repos that exist on the chosen
// host (a local-only repo is dropped for an SSH host).
type reposFilteredMsg struct {
	repos []string
}

const branchSearchDebounce = 150 * time.Millisecond

// scheduleBranchSearch returns a debounced tea.Cmd: sleeps, then triggers a search message.
func (m *home) scheduleBranchSearch(repoPath, filter string, version uint64) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(branchSearchDebounce)
		return branchSearchDebounceMsg{repoPath: repoPath, filter: filter, version: version}
	}
}

// runBranchSearch returns a tea.Cmd that performs the git search in the background.
// repoPath is the repository whose branches are listed (the instance's repo,
// never the process cwd).
func (m *home) runBranchSearch(repoPath, filter string, version uint64) tea.Cmd {
	return func() tea.Msg {
		branches, err := git.NewRepo(repoPath).SearchBranches(filter)
		if err != nil {
			log.WarningLog.Printf("branch search failed: %v", err)
			return nil
		}
		return branchSearchResultMsg{branches: branches, version: version}
	}
}

// instanceMetaResult holds the results of a single instance's metadata update,
// computed in a background goroutine.
type instanceMetaResult struct {
	instance  *session.Instance
	updated   bool
	status    program.Status
	stableFor time.Duration
	diffStats *git.DiffStats
}

// metadataUpdateDoneMsg is sent when the background metadata update completes.
type metadataUpdateDoneMsg struct {
	results []instanceMetaResult
}

// stableReadyThreshold is the agent-agnostic fallback: when an adapter
// returns no authoritative ready signal (StatusUnknown, or StatusWorking
// without a turn-boundary marker — e.g. an agent whose sentinel extension
// isn't installed), the instance is presumed idle once its tmux pane has been
// stable for this long. An adapter's explicit StatusReady/StatusPermission
// always wins and is immediate; this is only the net that catches every
// harness without a dedicated adapter. Tuned conservatively to avoid tripping
// on long silent tool runs (build/test with no output); the false-positive
// self-corrects the moment the tool emits a line.
const stableReadyThreshold = 60 * time.Second

// readyToWorkingTicks is the hysteresis threshold for the Ready→Running
// transition: a Ready instance is only demoted to Running after this many
// CONSECUTIVE "pane changed" ticks (each tick is ~500ms, so a value of 3 ≈
// 1.5s of sustained work). This suppresses the Ready↔Running flicker an idle
// instance shows because its pane chrome (Pi's animated spinner, the
// context-usage percentage, the cursor) keeps hashing differently every
// tick even though the agent is producing nothing. A single jitter tick does
// not flip; the streak resets on an authoritative Ready signal or a stable
// pane. A Running→Ready transition is NOT gated (a Ready signal is
// immediate). See the reconciliation loop in handleMetadataUpdate.
const readyToWorkingTicks = 3

// snapshotActiveInstances returns the currently active (started, not paused)
// instances. Called on the main thread so the filtering doesn't race with
// state mutations.
func (m *home) snapshotActiveInstances() []*session.Instance {
	var out []*session.Instance
	for _, inst := range m.list.GetInstances() {
		if inst.Started() && !inst.Paused() {
			out = append(out, inst)
		}
	}
	return out
}

// tickUpdateMetadataCmd returns a self-chaining Cmd that sleeps 500ms, then performs
// expensive metadata I/O (tmux capture, git diff) in parallel background goroutines.
// Because it only re-schedules after completing, overlapping ticks are impossible.
// The active instances slice should be snapshotted on the main thread via
// snapshotActiveInstances() before being passed here.
//
// Only the selected instance gets a full diff (with Content); the rest get a
// lightweight numstat-only summary. This keeps per-instance memory bounded
// since the diff pane only ever renders the selected one.
func tickUpdateMetadataCmd(active []*session.Instance, selected *session.Instance) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(500 * time.Millisecond)

		if len(active) == 0 {
			return metadataUpdateDoneMsg{}
		}

		results := make([]instanceMetaResult, len(active))
		var wg sync.WaitGroup
		for idx, inst := range active {
			wg.Add(1)
			go func(i int, instance *session.Instance) {
				defer wg.Done()
				r := &results[i]
				r.instance = instance
				r.updated, r.status, r.stableFor = instance.HasUpdated()
				if instance == selected {
					r.diffStats = instance.ComputeDiff()
				} else {
					r.diffStats = instance.ComputeDiffNumstat()
				}
			}(idx, inst)
		}
		wg.Wait()

		return metadataUpdateDoneMsg{results: results}
	}
}

// handleError handles all errors which get bubbled up to the app. sets the error message. We return a callback tea.Cmd that returns a hideErrMsg message
// which clears the error message after 3 seconds.
func (m *home) handleError(err error) tea.Cmd {
	log.ErrorLog.Printf("%v", err)
	m.errBox.SetError(err)
	return func() tea.Msg {
		select {
		case <-m.ctx.Done():
		case <-time.After(3 * time.Second):
		}

		return hideErrMsg{}
	}
}

// notifyReady fires a desktop notification when an instance transitions to
// Ready. Best-effort: failures are logged, never surfaced to the user, since a
// missing notification must never break the TUI loop. Runs in a background
// goroutine so the shell-out never blocks rendering.
func (m *home) notifyReady(instance *session.Instance) {
	title := fmt.Sprintf("boulez: %s ready", instance.Title)
	body := fmt.Sprintf("Instance '%s' finished and is waiting for input.", instance.Title)
	go func() {
		var c *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			c = exec.Command("osascript", "-e",
				fmt.Sprintf("display notification %q with title %q", body, title))
		case "linux":
			c = exec.Command("notify-send", title, body)
		default:
			return // unsupported, silently skip
		}
		if err := c.Run(); err != nil {
			log.WarningLog.Printf("notify ready failed for %s: %v", instance.Title, err)
		}
	}()
}

func (m *home) newPromptOverlay(repoPath string) *overlay.TextInputOverlay {
	o := overlay.NewTextInputOverlayWithBranchPicker("Enter prompt", "", m.appConfig.GetProfiles())
	// Preselect the profile stored as a preference for this repo (if any). A
	// stale/unknown preference name is ignored by SetSelectedByName, so the
	// picker falls back to the default profile rather than breaking.
	if m.prefs != nil && repoPath != "" {
		if pref, ok, _ := m.prefs.Get(repoPath); ok && pref.Profile != "" {
			o.PreselectProfile(pref.Profile)
		}
	}
	return o
}

// saveProfilePreference records the currently-selected profile in the prompt
// overlay as the explicit repo→profile preference for the selected
// instance's repo. Triggered by ctrl+s on the profile picker. Best-effort.
func (m *home) saveProfilePreference() tea.Cmd {
	inst := m.list.GetSelectedInstance()
	if inst == nil || m.textInputOverlay == nil || m.prefs == nil {
		return nil
	}
	profile := m.textInputOverlay.GetSelectedProgram()
	name := m.textInputOverlay.GetSelectedProfileName()
	if name == "" {
		return nil
	}
	if err := m.prefs.Set(inst.Path, name, profile); err != nil {
		return m.handleError(err)
	}
	return nil
}

// cancelPromptOverlay cancels the prompt overlay, cleaning up unstarted instances.
func (m *home) cancelPromptOverlay() tea.Cmd {
	selected := m.list.GetSelectedInstance()
	if selected != nil && !selected.Started() {
		m.untrackDraft(selected.GetID())
		m.list.Kill()
	}
	m.textInputOverlay = nil
	m.state = stateDefault
	return tea.Sequence(
		tea.WindowSize(),
		func() tea.Msg {
			m.menu.SetState(ui.StateDefault)
			return nil
		},
	)
}

// confirmAction shows a confirmation modal and stores the action to execute on
// confirm. The action is a tea.Cmd: on confirm it is returned through the
// overlay's OnConfirm callback and dispatched by the stateConfirm handler in
// handleKeyPress, so the action's outcome (a tea.Msg) reaches Update. This is
// what lets a confirm action run asynchronously and report success/error back
// to the TUI — previously the returned Cmd was discarded.
func (m *home) confirmAction(message string, action tea.Cmd) tea.Cmd {
	m.state = stateConfirm

	// Create and show the confirmation overlay using ConfirmationOverlay
	m.confirmationOverlay = overlay.NewConfirmationOverlay(message)
	// Set a fixed width for consistent appearance
	m.confirmationOverlay.SetWidth(50)

	// On confirm, transition back to default and return the action so the
	// stateConfirm handler dispatches it. The action runs as a tea.Cmd; if it
	// is a func() tea.Msg, the returned msg is delivered to Update.
	m.confirmationOverlay.OnConfirm = func() tea.Cmd {
		m.state = stateDefault
		return action
	}

	m.confirmationOverlay.OnCancel = func() tea.Cmd {
		m.state = stateDefault
		return nil
	}

	return nil
}

// landDoneMsg is the outcome of an async land (commit+push+merge via the
// kernel). It carries the instance ID (to clear landInFlight and the
// Landing hint), the result, and any error. A conflict is reported as a
// non-nil error with res.Merge.Status == MergeConflict, exactly as
// session.LandInstance returns it; handleLandDone surfaces the conflict files
// rather than a generic error. HostSynced/HostSyncNote come from the kernel
// (added in a follow-up); when the kernel does not populate them they default
// to false/"" and the message degrades gracefully to "Landed".
type landDoneMsg struct {
	instanceID string
	title      string
	target     string
	result     session.LandResult
	err        error
}

// runLandCmd issues session.LandInstance in a background goroutine and returns
// landDoneMsg. It is the async replacement for the old synchronous landAction:
// the (commit+push+merge) syscall no longer blocks the TUI loop, and its
// outcome reaches Update as a dedicated msg so the user gets visible feedback
// (success / conflict / error) instead of nothing. The instance's Landing hint
// is set by the caller (KeyLand) before dispatching; cleared here on completion.
func (m *home) runLandCmd(inst *session.Instance, caller session.LandCaller, target, commitMsg string) tea.Cmd {
	return func() tea.Msg {
		res, err := session.LandInstance(inst, caller, target, commitMsg)
		return landDoneMsg{
			instanceID: inst.GetID(),
			title:      inst.Title,
			target:     target,
			result:     res,
			err:        err,
		}
	}
}

// handleLandDone processes a landDoneMsg: clears the in-flight + Landing
// hints, surfaces the outcome in the error box, and marks the instance Landed
// on success. On conflict it lists the conflicted files and the throwaway
// worktree path (left in the merging state for resolution). On any outcome it
// refreshes the fleet so the view reflects the merged main (or the conflict).
func (m *home) handleLandDone(msg landDoneMsg) tea.Cmd {
	delete(m.landInFlight, msg.instanceID)
	if inst := m.list.FindInstance(msg.instanceID); inst != nil {
		inst.SetLanding(false)
	}

	if msg.err != nil {
		// A real merge conflict carries conflicted files and/or a merging
		// worktree path. A refusal (e.g. ErrHostOnTargetBranchDirty) reaches
		// here with MergeConflict status (fabricated by the socket adapter on
		// any wire error) but NO conflicts and NO worktree path — surface the
		// raw error instead so the actionable message ("commit, stash, or
		// switch branches before landing") is shown rather than an empty
		// "merge conflict on <target>: ".
		if msg.result.Merge.Status == git.MergeConflict &&
			(len(msg.result.Merge.Conflicts) > 0 || msg.result.Merge.WorktreePath != "") {
			files := make([]string, 0, len(msg.result.Merge.Conflicts))
			for _, c := range msg.result.Merge.Conflicts {
				files = append(files, c.File)
			}
			extra := ""
			if msg.result.Merge.WorktreePath != "" {
				extra = fmt.Sprintf(" — repo left in merging state at %s; resolve and `git commit`", msg.result.Merge.WorktreePath)
			}
			return tea.Batch(
				m.handleError(fmt.Errorf("merge conflict on %s%s: %s",
					msg.target, extra, strings.Join(files, ", "))),
				m.refreshFleetAfterMutation(),
			)
		}
		return tea.Batch(m.handleError(msg.err), m.refreshFleetAfterMutation())
	}

	// Success: surface a clear message (green-tinted via the host-sync note,
	// if present) and mark the instance as landed so the renderer dims it +
	// shows the checkmark until the agent resumes work (Running→Ready clears it).
	if inst := m.list.FindInstance(msg.instanceID); inst != nil {
		inst.SetLanded(true)
	}
	note := strings.TrimSpace(msg.result.HostSyncNote)
	var success error
	if msg.result.HostSynced {
		success = successMsg(fmt.Sprintf("Landed '%s' into %s (host synced)", msg.title, msg.target))
	} else if note != "" {
		success = successMsg(fmt.Sprintf("Landed '%s' into %s; %s", msg.title, msg.target, note))
	} else {
		success = successMsg(fmt.Sprintf("Landed '%s' into %s", msg.title, msg.target))
	}
	return tea.Batch(m.handleError(success), m.refreshFleetAfterMutation())
}

// successMsg is a non-error tea.Msg that handleError still surfaces in the error
// box, so a successful land reads as a positive (green) line rather than red.
// It implements error only to ride the existing case error: path in Update;
// handleError special-cases successMsg to render without the error styling.
type successMsg string

func (s successMsg) Error() string { return string(s) }

// startNewInstance creates a new instance bound to repoPath, registers it in
// the list, and enters the name-entry state. When promptFlow is true, the
// prompt+branch overlay follows name entry, and a background branch fetch is
// kicked off so branches are fresh by the time the picker opens.
func (m *home) startNewInstance(repoPath string, promptFlow bool) tea.Cmd {
	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   "",
		Path:    repoPath,
		Program: m.agentProgram,
	})
	if err != nil {
		return m.handleError(err)
	}

	m.newInstanceFinalizer = m.list.AddInstance(instance)
	m.trackDraft(instance.GetID())
	m.list.SetSelectedInstance(m.list.NumInstances() - 1)
	// Apply the chosen host before Start binds tmux/git deps. SetHost refuses
	// after Start, so this must happen here (name entry → Start).
	if m.pendingHost != nil {
		if err := instance.SetHost(m.pendingHost); err != nil {
			return m.handleError(err)
		}
		m.pendingHost = nil
	}
	m.state = stateNew
	m.menu.SetState(ui.StateNewInstance)
	m.promptAfterName = promptFlow

	if promptFlow {
		// Best-effort background fetch so the branch picker is up to date.
		// Use the instance's host executor so a remote repo is fetched remotely.
		h := instance.Host()
		repoPath := instance.Path
		exec := h.Executor()
		return func() tea.Msg {
			git.NewRepoWithDeps(repoPath, exec).FetchBranches()
			return nil
		}
	}
	return nil
}

// spawnOrchestrator handles the O key: it spawns an orchestrator instance
// through the kernel's spawn_worker syscall (C3.3). This is the manual
// replacement for the old always-on "instance 0" bootstrap — nothing is
// auto-spawned at startup; the user spawns one when they want one.
//
// An orchestrator is an ordinary fleet instance with KindOrchestrator: a
// headless worktree (no repo, no branch) whose control dir holds
// ORCHESTRATOR.md (the agent's tool documentation). The kernel creates and
// starts the instance; on ack the TUI writes ORCHESTRATOR.md into the control
// dir and injects a one-time prompt pointing the agent at it (plus a fleet
// snapshot). Each O press spawns a fresh orchestrator; the user kills one
// with D like any other instance.
//
// The instance is spawned via the kernel socket (not session.NewInstance
// directly), so the kernel is the single writer. The TUI keeps a draft in
// the list (showing Loading) while the syscall is in flight.
func (m *home) spawnOrchestrator() tea.Cmd {
	title := deriveOrchestratorTitle()
	// Draft instance for the view (Loading) while the kernel spawns. Its ID is
	// local-only; the kernel allocates the real ID. On the spawn ack the draft
	// is removed and the kernel's instance surfaces via the fleet refresh.
	draft, err := session.NewInstance(session.InstanceOptions{
		Title:   title,
		Program: m.agentProgram,
		Kind:    session.KindOrchestrator,
	})
	if err != nil {
		return m.handleError(err)
	}
	draft.SetStatus(session.Loading)
	finalize := m.list.AddInstance(draft)
	m.trackDraft(draft.GetID())
	m.list.SetSelectedInstance(m.list.NumInstances() - 1)
	finalize() // orchestrator has no repo, so the repo-name registration is a no-op

	return tea.Batch(
		tea.WindowSize(),
		m.instanceChanged(),
		m.runSpawnCmd(SpawnOptions{
			Title:   title,
			Program: m.agentProgram,
			Kind:    session.KindOrchestrator,
		}, draft.GetID(), true /* orchestrator post-spawn injection */),
	)
}

// injectOrchestratorContext writes ORCHESTRATOR.md into the orchestrator's
// control dir and sends the one-time injection prompt. Run on the spawn ack
// (the instance is now started by the kernel). Best-effort: a failure here
// surfaces as an error but does not undo the spawn.
func (m *home) injectOrchestratorContext(id, title string) tea.Cmd {
	return func() tea.Msg {
		if err := orchestrator.WriteContextFile(id); err != nil {
			return fmt.Errorf("write orchestrator context: %w", err)
		}
		// Find the live instance (now in the TUI's reconciled cache) to send the
		// injection prompt and to render the fleet snapshot.
		inst := m.list.FindInstance(id)
		if inst == nil {
			// Instance gone between ack and injection (e.g. killed). Non-fatal.
			return nil
		}
		fleet := orchestrator.RenderFleet(toOrchestratorFleet(m.list.GetInstances()))
		if err := inst.SendPrompt(orchestrator.InjectionPrompt(fleet)); err != nil {
			return fmt.Errorf("inject orchestrator prompt: %w", err)
		}
		return nil
	}
}

// selectInstanceByTitle selects the first instance whose Title matches, used
// to land the selection on a freshly-spawned instance (the kernel allocates
// its own ID, so the TUI finds it by the title it requested).
func (m *home) selectInstanceByTitle(title string) {
	for i, inst := range m.list.GetInstances() {
		if inst.Title == title {
			m.list.SetSelectedInstance(i)
			return
		}
	}
}

// toOrchestratorFleet projects the TUI's []*session.Instance into the
// decoupled []orchestrator.Instance type the bootstrap/prompt helpers expect
// (they live in the orchestrator package, which deliberately does not import
// session). The projection stays here at the seam.
func toOrchestratorFleet(instances []*session.Instance) []orchestrator.Instance {
	out := make([]orchestrator.Instance, 0, len(instances))
	for _, in := range instances {
		if in == nil {
			continue
		}
		repoName, _ := in.RepoName()
		out = append(out, orchestrator.Instance{
			ID:      in.ID,
			Kind:    in.Kind().String(),
			Status:  in.Status.String(),
			Title:   in.Title,
			Repo:    repoName,
			Branch:  in.Branch,
			Program: in.Program,
			Host:    in.Host().Name(),
		})
	}
	return out
}

// deriveOrchestratorTitle builds a unique title for a manually-spawned
// orchestrator. The title drives the tmux session name, so it must be unique
// across spawns to avoid collisions with a lingering session from a previous
// orchestrator.
func deriveOrchestratorTitle() string {
	return fmt.Sprintf("orchestrator-%d", time.Now().UnixNano())
}
