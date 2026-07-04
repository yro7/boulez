package app

import (
	"claude-squad/config"
	"claude-squad/host"
	"claude-squad/keys"
	"claude-squad/log"
	"claude-squad/orchestrator"
	"claude-squad/prefs"
	"claude-squad/presets"
	"claude-squad/program"
	"claude-squad/repo"
	"claude-squad/session"
	"claude-squad/session/git"
	"claude-squad/ui"
	"claude-squad/ui/overlay"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
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
)

type home struct {
	ctx context.Context

	// -- Storage and Configuration --

	program string
	autoYes bool

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
	// startingInstance is retained for back-compat with callers that used to
	// pass it to runInstanceStartCmd; it is now unused (spawn goes through the
	// kernel) but kept here so a bare &home{} test construct that sets it
	// still compiles. Phase 4 removes it.
	startingInstance *session.Instance

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
	// ~/.cs2/presets.json between two opens with no watcher.
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
		ctx:          ctx,
		spinner:      spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		menu:         ui.NewMenu(),
		tabbedWindow: ui.NewTabbedWindow(ui.NewPreviewPane(), ui.NewDiffPane(), ui.NewTerminalPane()),
		errBox:       ui.NewErrBox(),
		appConfig:    appConfig,
		repoRegistry: repoRegistry,
		hostRegistry: hostRegistry,
		prefs:        prefStore,
		presetStore:  presetStore,
		program:      program,
		autoYes:      autoYes,
		state:        stateDefault,
		appState:     appState,
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
	case previewTickMsg:
		cmd := m.instanceChanged()
		return m, tea.Batch(
			cmd,
			func() tea.Msg {
				time.Sleep(100 * time.Millisecond)
				return previewTickMsg{}
			},
		)
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
			switch {
			case r.status == program.StatusReady:
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
				// fall back to the content-change heuristic so unknown agents
				// keep cycling Running/Loading like before.
				if r.updated {
					r.instance.SetStatus(session.Running)
				}
			}
			// Notify on the Ready transition when configured.
			if prevStatus != session.Ready && r.instance.Status == session.Ready {
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
	case spawnDoneMsg:
		// C3.3: spawn routed through the kernel. On success the kernel created
		// and started the instance; the TUI re-reads the fleet (C3.2) to pick it
		// up. The draft instance held in the list during name entry is removed
		// because the kernel owns the real instance now (with its own ID).
		if msg.draftID != "" {
			m.list.RemoveByID(msg.draftID)
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
	if m.state == statePrompt || m.state == stateHelp || m.state == stateConfirm || m.state == stateRepoSelect || m.state == stateHostSelect || m.state == statePresetSelect {
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
		// Handle quit commands first. Don't handle q because the user might want to type that.
		if msg.String() == "ctrl+c" {
			m.state = stateDefault
			m.promptAfterName = false
			m.list.Kill()
			return m, tea.Sequence(
				tea.WindowSize(),
				func() tea.Msg {
					m.menu.SetState(ui.StateDefault)
					return nil
				},
			)
		}

		instance := m.list.GetInstances()[m.list.NumInstances()-1]
		switch msg.Type {
		// Start the instance (enable previews etc) and go back to the main menu state.
		case tea.KeyEnter:
			if len(instance.Title) == 0 {
				return m, m.handleError(fmt.Errorf("title cannot be empty"))
			}

			// If promptAfterName, show prompt+branch overlay before starting
			if m.promptAfterName {
				m.promptAfterName = false
				m.state = statePrompt
				m.menu.SetState(ui.StatePrompt)
				m.textInputOverlay = m.newPromptOverlay(instance.Path)
				// Trigger initial branch search (no debounce, version 0) on the
				// instance's repo, not the process cwd.
				repoPath := instance.Path
				initialSearch := m.runBranchSearch(repoPath, "", m.textInputOverlay.BranchFilterVersion())
				return m, tea.Batch(tea.WindowSize(), initialSearch)
			}

			// Set Loading status and finalize into the list immediately
			instance.SetStatus(session.Loading)
			m.newInstanceFinalizer()
			m.promptAfterName = false
			m.state = stateDefault
			m.menu.SetState(ui.StateDefault)

			// Route spawn through the kernel (C3.3): the TUI keeps the draft
			// (Loading) in the list while the syscall is in flight; on ack the
			// draft is removed and the kernel's instance surfaces via the fleet
			// refresh.
			opts := SpawnOptions{
				Repo:    instance.Path,
				Title:   instance.Title,
				Program: instance.Program,
				Branch:  instance.SelectedBranch(),
			}
			if m.pendingHost != nil {
				opts.Host = m.pendingHost
				m.pendingHost = nil
			}
			return m, tea.Batch(tea.WindowSize(), m.instanceChanged(), m.runSpawnCmd(opts, instance.GetID(), false))
		case tea.KeyRunes:
			if runewidth.StringWidth(instance.Title) >= 32 {
				return m, m.handleError(fmt.Errorf("title cannot be longer than 32 characters"))
			}
			if err := instance.SetTitle(instance.Title + string(msg.Runes)); err != nil {
				return m, m.handleError(err)
			}
		case tea.KeyBackspace:
			runes := []rune(instance.Title)
			if len(runes) == 0 {
				return m, nil
			}
			if err := instance.SetTitle(string(runes[:len(runes)-1])); err != nil {
				return m, m.handleError(err)
			}
		case tea.KeySpace:
			if err := instance.SetTitle(instance.Title + " "); err != nil {
				return m, m.handleError(err)
			}
		case tea.KeyEsc:
			m.list.Kill()
			m.state = stateDefault
			m.instanceChanged()

			return m, tea.Sequence(
				tea.WindowSize(),
				func() tea.Msg {
					m.menu.SetState(ui.StateDefault)
					return nil
				},
			)
		default:
		}
		return m, nil
	} else if m.state == statePrompt {
		// Handle cancel via ctrl+c before delegating to the overlay
		if msg.String() == "ctrl+c" {
			return m, m.cancelPromptOverlay()
		}

		// ctrl+s on the profile picker records an explicit repo→profile
		// preference for the selected instance's repo, so the prompt overlay
		// preselects this profile next time. Best-effort: a nil prefs store
		// or a failure to persist never blocks the prompt flow.
		if msg.String() == "ctrl+s" {
			return m, m.saveProfilePreference()
		}

		// Use the new TextInputOverlay component to handle all key events
		shouldClose, branchFilterChanged := m.textInputOverlay.HandleKeyPress(msg)

		// Check if the form was submitted or canceled
		if shouldClose {
			selected := m.list.GetSelectedInstance()
			if selected == nil {
				return m, nil
			}

			if m.textInputOverlay.IsCanceled() {
				return m, m.cancelPromptOverlay()
			}

			if m.textInputOverlay.IsSubmitted() {
				prompt := m.textInputOverlay.GetValue()
				selectedBranch := m.textInputOverlay.GetSelectedBranch()
				selectedProgram := m.textInputOverlay.GetSelectedProgram()

				if !selected.Started() {
					// Shift+N flow: instance not started yet — hand the prompt+
					// branch off to the kernel spawn syscall (C3.3). The draft
					// stays Loading while the kernel creates the real instance.
					if selectedBranch != "" {
						selected.SetSelectedBranch(selectedBranch)
					}
					if selectedProgram != "" {
						selected.Program = selectedProgram
					}
					selected.Prompt = prompt

					// Finalize into list and spawn via the kernel.
					selected.SetStatus(session.Loading)
					m.newInstanceFinalizer()
					m.textInputOverlay = nil
					m.state = stateDefault
					m.menu.SetState(ui.StateDefault)

					opts := SpawnOptions{
						Repo:    selected.Path,
						Title:   selected.Title,
						Program: selected.Program,
						Branch:  selected.SelectedBranch(),
						Prompt:  prompt,
					}
					return m, tea.Batch(tea.WindowSize(), m.instanceChanged(), m.runSpawnCmd(opts, selected.GetID(), false))
				}

				// Regular flow: instance already running, just send prompt
				if err := selected.SendPrompt(prompt); err != nil {
					return m, m.handleError(err)
				}
			}

			// Close the overlay and reset state
			m.textInputOverlay = nil
			m.state = stateDefault
			return m, tea.Sequence(
				tea.WindowSize(),
				func() tea.Msg {
					m.menu.SetState(ui.StateDefault)
					m.showHelpScreen(helpStart(selected), nil)
					return nil
				},
			)
		}

		// Schedule a debounced branch search if the filter changed
		if branchFilterChanged {
			filter := m.textInputOverlay.BranchFilter()
			version := m.textInputOverlay.BranchFilterVersion()
			repoPath := ""
			if selected := m.list.GetSelectedInstance(); selected != nil {
				repoPath = selected.Path
			}
			return m, m.scheduleBranchSearch(repoPath, filter, version)
		}

		return m, nil
	}

	// Handle confirmation state
	if m.state == stateConfirm {
		shouldClose := m.confirmationOverlay.HandleKeyPress(msg)
		if shouldClose {
			m.state = stateDefault
			m.confirmationOverlay = nil
			return m, nil
		}
		return m, nil
	}

	// Exit scrolling mode when ESC is pressed and preview pane is in scrolling mode
	// Check if Escape key was pressed and we're not in the diff tab (meaning we're in preview tab)
	// Always check for escape key first to ensure it doesn't get intercepted elsewhere
	if msg.Type == tea.KeyEsc {
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

	// Handle quit commands first
	if msg.String() == "ctrl+c" || msg.String() == "q" {
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
			commitMsg := fmt.Sprintf("[claudesquad] update from '%s' on %s", selected.Title, time.Now().Format(time.RFC822))
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
		commitMsg := fmt.Sprintf("[claudesquad] update from '%s' on %s", inst.Title, time.Now().Format(time.RFC822))
		caller := m.landCaller
		if caller == nil {
			caller = newSocketLandCaller()
		}
		landAction := func() tea.Msg {
			res, err := session.LandInstance(inst, caller, targetBranch, commitMsg)
			if err != nil {
				if res.Merge.Status == git.MergeConflict {
					// Conflict is an expected, recoverable outcome: the repo is
					// left in the merging state for resolution. Surface the
					// conflicted files clearly rather than as a generic error.
					files := make([]string, 0, len(res.Merge.Conflicts))
					for _, c := range res.Merge.Conflicts {
						files = append(files, c.File)
					}
					return fmt.Errorf("merge conflict on %s — repo left in merging state. Resolve and `git commit`: %s",
							targetBranch, strings.Join(files, ", "))
				}
				return err
			}
			return nil
		}
		message := fmt.Sprintf("[!] Land '%s' into '%s'?\n(commit + push '%s' then merge into %s)",
			inst.Title, targetBranch, inst.Title, targetBranch)
		return m, m.confirmAction(message, landAction)
	case keys.KeyCheckout:
		selected := m.list.GetSelectedInstance()
		if selected == nil || selected.Status == session.Loading || selected.Status == session.Dead {
			return m, nil
		}

		// Show help screen before pausing
		m.showHelpScreen(helpTypeInstanceCheckout{}, func() {
			// Route pause through the kernel (C3.4): the kernel owns the tmux
			// session + worktree lifecycle, so the pause (and the persistence)
			// takes effect on the authoritative copy. The TUI's view is
			// reconciled by the post-mutation fleet refresh issued below.
			if err := m.resolveFleet().Pause(selected.GetID()); err != nil {
				m.handleError(err)
			}
			m.tabbedWindow.CleanupTerminalForInstance(selected.Title)
			m.instanceChanged()
			_ = m.refreshFleetFromKernel()
		})
		return m, tea.Batch(m.instanceChanged(), m.refreshFleetAfterMutation())
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
		// Terminal tab: attach to terminal session
		if m.tabbedWindow.IsInTerminalTab() {
			m.showHelpScreen(helpTypeInstanceAttach{}, func() {
				ch, err := m.tabbedWindow.AttachTerminal()
				if err != nil {
					m.handleError(err)
					return
				}
				<-ch
				m.state = stateDefault
			})
			return m, nil
		}
		// Show help screen before attaching
		m.showHelpScreen(helpTypeInstanceAttach{}, func() {
			ch, err := m.list.Attach()
			if err != nil {
				m.handleError(err)
				return
			}
			<-ch
			m.state = stateDefault
			m.instanceChanged()
		})
		return m, nil
	default:
		return m, nil
	}
}

// instanceChanged updates the preview pane, menu, and diff pane based on the selected instance. It returns an error
// Cmd if there was any error.
func (m *home) instanceChanged() tea.Cmd {
	// selected may be nil
	selected := m.list.GetSelectedInstance()

	m.tabbedWindow.UpdateDiff(selected)
	m.tabbedWindow.SetInstance(selected)
	// Update menu with current instance
	m.menu.SetInstance(selected)

	// If there's no selected instance, we don't need to update the preview.
	if err := m.tabbedWindow.UpdatePreview(selected); err != nil {
		return m.handleError(err)
	}
	if err := m.tabbedWindow.UpdateTerminal(selected); err != nil {
		return m.handleError(err)
	}
	return nil
}

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

// filterRepos returns a tea.Cmd that probes each registered repo against the
// chosen host's executor and returns only those that exist on that host. Local
// is instant; remote fans out concurrently (one `ssh host git -C <path> ...`
// per repo). The repo selector starts with the full list and is narrowed when
// this result lands, so local users see no flicker while remote users see the
// inaccessible entries drop once probed.
func (m *home) filterRepos(repos []string, h host.Host) tea.Cmd {
	return func() tea.Msg {
		return reposFilteredMsg{repos: git.FilterExistingRepos(repos, h.Executor())}
	}
}

// instanceMetaResult holds the results of a single instance's metadata update,
// computed in a background goroutine.
type instanceMetaResult struct {
	instance  *session.Instance
	updated   bool
	status    program.Status
	diffStats *git.DiffStats
}

// metadataUpdateDoneMsg is sent when the background metadata update completes.
type metadataUpdateDoneMsg struct {
	results []instanceMetaResult
}

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
				r.updated, r.status = instance.HasUpdated()
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
	title := fmt.Sprintf("cs2: %s ready", instance.Title)
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

// confirmAction shows a confirmation modal and stores the action to execute on confirm
func (m *home) confirmAction(message string, action tea.Cmd) tea.Cmd {
	m.state = stateConfirm

	// Create and show the confirmation overlay using ConfirmationOverlay
	m.confirmationOverlay = overlay.NewConfirmationOverlay(message)
	// Set a fixed width for consistent appearance
	m.confirmationOverlay.SetWidth(50)

	// Set callbacks for confirmation and cancellation
	m.confirmationOverlay.OnConfirm = func() {
		m.state = stateDefault
		// Execute the action if it exists
		if action != nil {
			_ = action()
		}
	}

	m.confirmationOverlay.OnCancel = func() {
		m.state = stateDefault
	}

	return nil
}

// openHostSelector opens the host selector overlay first, before the repo
// selector. promptFlow controls whether the prompt+branch overlay follows name
// entry (KeyPrompt) or not (KeyNew). The chosen host is stashed in pendingHost
// and carried into the repo selector.
func (m *home) openHostSelector(promptFlow bool) tea.Cmd {
	var aliases []string
	if m.hostRegistry != nil {
		aliases, _ = m.hostRegistry.List()
	}
	// Skip the selector when there is no real choice: 0 registered aliases
	// → local (always implicit), 1 alias → that alias. The selector only opens
	// when there are ≥2 options (local + ≥1 alias, or ≥2 aliases). Avoids a
	// gratuitous Enter in the common all-local case.
	if len(aliases) < 2 {
		alias := host.LocalAlias
		if len(aliases) == 1 {
			alias = aliases[0]
		}
		m.repoSelectPrompt = promptFlow
		m.pendingHost = host.Lookup(alias)
		return m.openRepoSelector(promptFlow)
	}
	m.hostSelector = overlay.NewHostSelector(aliases)
	m.repoSelectPrompt = promptFlow
	m.state = stateHostSelect
	return tea.WindowSize()
}

// handleHostSelectState dispatches key presses to the host selector overlay and
// finalizes the selection on submit/cancel, transitioning to the repo selector.
func (m *home) handleHostSelectState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.hostSelector == nil {
		m.state = stateDefault
		return m, nil
	}

	// ctrl+c cancels like esc.
	if msg.String() == "ctrl+c" {
		m.hostSelector.Canceled = true
	} else {
		shouldClose := m.hostSelector.HandleKeyPress(msg)
		// Apply silent ctrl+d removals to the persistent host registry. Done
		// on every non-close keypress (TakeDeletedValues is a no-op when
		// nothing was deleted); local is protected inside the selector.
		m.applyHostDeletions()
		if !shouldClose {
			return m, nil
		}
	}

	if m.hostSelector.Canceled {
		m.hostSelector = nil
		m.state = stateDefault
		return m, tea.WindowSize()
	}

	// Submit: resolve the alias to a Host.
	alias := m.hostSelector.SelectedAlias()
	if alias == "" {
		m.hostSelector.Submitted = false
		return m, m.handleError(fmt.Errorf("please select a host or type an alias"))
	}

	// If the alias was typed freely (not picked from the registry and not
	// local), register it so it reappears next time. Best-effort.
	if m.hostSelector.IsFreeAlias() && alias != "local" && m.hostRegistry != nil {
		_ = m.hostRegistry.Add(alias)
	}
	// MRU: move the selected alias to the head of the registry so it is
	// offered at the top next time. Best-effort; local is a no-op.
	if alias != "local" && m.hostRegistry != nil {
		_ = m.hostRegistry.Touch(alias)
	}

	promptFlow := m.repoSelectPrompt
	m.pendingHost = host.Lookup(alias)
	m.hostSelector = nil
	// Proceed to the repo selector, which will validate the repo path using
	// the chosen host's executor (so a remote repo path is checked remotely).
	return m, m.openRepoSelector(promptFlow)
}

// openRepoSelector opens the repo selector overlay before creating a new
// instance. promptFlow controls whether the prompt+branch overlay follows name
// entry (KeyPrompt) or not (KeyNew). The repo path is validated against
// m.pendingHost's executor (local or ssh).
func (m *home) openRepoSelector(promptFlow bool) tea.Cmd {
	var repos []string
	if m.repoRegistry != nil {
		repos, _ = m.repoRegistry.List()
	}
	m.repoSelector = overlay.NewRepoSelector(repos)
	m.repoSelectPrompt = promptFlow
	m.state = stateRepoSelect

	// Filter the registered repos against the chosen host's executor: a remote
	// host can only run instances from repos that exist on that machine, so a
	// local-only repo is hidden rather than offered (and rejected at submit).
	// Local is instant; remote fans out one `ssh host git -C <path> ...` per
	// repo concurrently. The selector starts with the full list and is
	// narrowed when the result lands — local users see no flicker, remote
	// users see the inaccessible entries drop once probed.
	h := m.pendingHost
	if h == nil {
		h = host.Local
	}
	return tea.Batch(tea.WindowSize(), m.filterRepos(repos, h))
}

// handleRepoSelectState dispatches key presses to the repo selector overlay
// and finalizes the selection on submit/cancel.
func (m *home) handleRepoSelectState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.repoSelector == nil {
		m.state = stateDefault
		return m, nil
	}

	// ctrl+c cancels like esc.
	if msg.String() == "ctrl+c" {
		m.repoSelector.Canceled = true
	} else {
		shouldClose := m.repoSelector.HandleKeyPress(msg)
		// Apply silent ctrl+d removals to the persistent repo registry. Done
		// on every non-close keypress (TakeDeletedValues is a no-op when
		// nothing was deleted).
		m.applyRepoDeletions()
		if !shouldClose {
			return m, nil
		}
	}

	if m.repoSelector.Canceled {
		m.repoSelector = nil
		m.state = stateDefault
		return m, tea.WindowSize()
	}

	// Submit: validate the chosen path against the selected host's executor
	// (so a remote repo path is checked on the right machine).
	h := m.pendingHost
	if h == nil {
		h = host.Local
	}
	selected := m.repoSelector.SelectedPath()
	if selected == "" {
		m.repoSelector.Submitted = false
		return m, m.handleError(fmt.Errorf("please select a repo or type a path"))
	}
	if !git.NewRepoWithDeps(selected, h.Executor()).IsGitRepo() {
		m.repoSelector.Submitted = false
		return m, m.handleError(fmt.Errorf("not a git repository: %s", selected))
	}

	// If the path was typed freely (not picked from the registry), register it
	// so it reappears next time. Best-effort: a failure here does not block
	// instance creation.
	if m.repoSelector.IsFreePath() && m.repoRegistry != nil {
		_ = m.repoRegistry.Add(selected)
	}
	// MRU: move the selected repo to the head of the registry so it is
	// offered at the top next time. Best-effort.
	if m.repoRegistry != nil {
		_ = m.repoRegistry.Touch(selected)
	}

	promptFlow := m.repoSelectPrompt
	repoPath := selected
	m.repoSelector = nil
	return m, m.startNewInstance(repoPath, promptFlow)
}

// applyHostDeletions persists any hosts removed via ctrl+d in the host
// selector. Silent and best-effort: a failure to write does not block the
// selection flow. "local" is never deletable (protected in ListSelector).
func (m *home) applyHostDeletions() {
	if m.hostSelector == nil || m.hostRegistry == nil {
		return
	}
	for _, alias := range m.hostSelector.TakeDeletedValues() {
		_ = m.hostRegistry.Remove(alias)
	}
}

// applyRepoDeletions persists any repos removed via ctrl+d in the repo
// selector. Silent and best-effort.
func (m *home) applyRepoDeletions() {
	if m.repoSelector == nil || m.repoRegistry == nil {
		return
	}
	for _, path := range m.repoSelector.TakeDeletedValues() {
		_ = m.repoRegistry.Remove(path)
	}
}

// startNewInstance creates a new instance bound to repoPath, registers it in
// the list, and enters the name-entry state. When promptFlow is true, the
// prompt+branch overlay follows name entry, and a background branch fetch is
// kicked off so branches are fresh by the time the picker opens.
func (m *home) startNewInstance(repoPath string, promptFlow bool) tea.Cmd {
	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   "",
		Path:    repoPath,
		Program: m.program,
	})
	if err != nil {
		return m.handleError(err)
	}

	m.newInstanceFinalizer = m.list.AddInstance(instance)
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
		Program: m.program,
		Kind:    session.KindOrchestrator,
	})
	if err != nil {
		return m.handleError(err)
	}
	draft.SetStatus(session.Loading)
	finalize := m.list.AddInstance(draft)
	m.list.SetSelectedInstance(m.list.NumInstances() - 1)
	finalize() // orchestrator has no repo, so the repo-name registration is a no-op

	return tea.Batch(
		tea.WindowSize(),
		m.instanceChanged(),
		m.runSpawnCmd(SpawnOptions{
			Title:   title,
			Program: m.program,
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

// openPresetSelector opens the named-preset picker (Ctrl+R). Presets are read
// fresh from ~/.cs2/presets.json on every open, so an agent or editor can
// change the file between two opens with no watcher. An empty store is shown
// as an error pointing at the file rather than an empty picker.
func (m *home) openPresetSelector() tea.Cmd {
	var names []string
	if m.presetStore != nil {
		names, _ = m.presetStore.List()
	}
	if len(names) == 0 {
		path := "~/.cs2/presets.json"
		if m.presetStore != nil {
			path = m.presetStore.Path()
		}
		return m.handleError(fmt.Errorf("no presets defined — add one to %s", path))
	}
	m.presetSelector = overlay.NewPresetSelector(names)
	m.state = statePresetSelect
	return tea.WindowSize()
}

// handlePresetSelectState dispatches key presses to the preset selector overlay
// and finalizes the selection on submit/cancel. On submit it resolves the
// preset to a host + repo + profile + branch + prompt, validates them against
// the registries/config, and jumps straight to name entry (stateNew), skipping
// the host/repo/prompt overlays.
func (m *home) handlePresetSelectState(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.presetSelector == nil {
		m.state = stateDefault
		return m, nil
	}

	if msg.String() == "ctrl+c" {
		m.presetSelector.Canceled = true
	} else {
		shouldClose := m.presetSelector.HandleKeyPress(msg)
		if !shouldClose {
			return m, nil
		}
	}

	if m.presetSelector.Canceled {
		m.presetSelector = nil
		m.state = stateDefault
		return m, tea.WindowSize()
	}

	name := m.presetSelector.SelectedPreset()
	if name == "" {
		m.presetSelector.Submitted = false
		return m, m.handleError(fmt.Errorf("please select a preset"))
	}

	if m.presetStore == nil {
		m.presetSelector = nil
		m.state = stateDefault
		return m, m.handleError(fmt.Errorf("preset store unavailable"))
	}
	preset, ok, err := m.presetStore.Get(name)
	if err != nil || !ok {
		m.presetSelector = nil
		m.state = stateDefault
		return m, m.handleError(fmt.Errorf("preset %q not found", name))
	}

	m.presetSelector = nil
	m.state = stateDefault
	return m, m.startNewInstanceFromPreset(name, preset)
}

// startNewInstanceFromPreset applies a preset's host/repo/profile/branch and
// jumps straight to name entry (stateNew). The prompt overlay selectors are
// skipped entirely. Validation mirrors the normal flow: the repo must be a
// git repo (checked against the preset's host executor), and the profile name
// must resolve to a known config.Profile. The prompt, if any, is stashed on
// the instance and sent after Start.
func (m *home) startNewInstanceFromPreset(name string, p presets.Preset) tea.Cmd {
	repoPath := p.Repo
	if repoPath == "" {
		return m.handleError(fmt.Errorf("preset %q: repo is required", name))
	}

	// Resolve the host. "local" / empty → Local; anything else is treated as
	// an ssh alias (Lookup constructs an SSHHost regardless of registry
	// membership — a preset is an explicit recipe, not a registry mutation).
	h := host.Lookup(p.Host)

	// Validate the repo against the chosen host's executor (so a remote repo
	// is checked on the right machine), mirroring handleRepoSelectState.
	if !git.NewRepoWithDeps(repoPath, h.Executor()).IsGitRepo() {
		return m.handleError(fmt.Errorf("preset %q: not a git repository: %s", name, repoPath))
	}

	// Resolve the profile name to a program string. An empty profile means
	// "use the default program" (the cs2 --program flag). A name that matches
	// no profile is rejected so a stale preset does not start a wrong agent.
	program := m.program
	if p.Profile != "" {
		resolved, ok := m.appConfig.GetProfileByName(p.Profile)
		if !ok {
			return m.handleError(fmt.Errorf("preset %q: unknown profile %q", name, p.Profile))
		}
		program = resolved
	}

	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   "",
		Path:    repoPath,
		Program: program,
	})
	if err != nil {
		return m.handleError(err)
	}
	if err := instance.SetHost(h); err != nil {
		return m.handleError(err)
	}
	if p.Branch != "" {
		instance.SetSelectedBranch(p.Branch)
	}
	if p.Prompt != "" {
		instance.Prompt = p.Prompt
	}

	m.newInstanceFinalizer = m.list.AddInstance(instance)
	m.list.SetSelectedInstance(m.list.NumInstances() - 1)
	m.pendingHost = nil
	// A preset is a complete recipe: the host/repo/prompt selectors are
	// skipped entirely. Name entry is the only remaining step. The prompt,
	// if any, is stashed on the instance and auto-sent after Start by the
	// instanceStartedMsg handler (the same path the Shift+N flow uses), so
	// no prompt overlay is shown — that is the point of a quick session.
	if p.Prompt != "" {
		instance.Prompt = p.Prompt
	}
	m.promptAfterName = false
	m.state = stateNew
	m.menu.SetState(ui.StateNewInstance)
	return tea.WindowSize()
}

func (m *home) View() string {
	listWithPadding := lipgloss.NewStyle().PaddingTop(1).Render(m.list.String())
	previewWithPadding := lipgloss.NewStyle().PaddingTop(1).Render(m.tabbedWindow.String())
	listAndPreview := lipgloss.JoinHorizontal(lipgloss.Top, listWithPadding, previewWithPadding)

	mainView := lipgloss.JoinVertical(
		lipgloss.Center,
		listAndPreview,
		m.menu.String(),
		m.errBox.String(),
	)

	if m.state == statePrompt {
		if m.textInputOverlay == nil {
			log.ErrorLog.Printf("text input overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.textInputOverlay.Render(), mainView, true, true)
	} else if m.state == stateHelp {
		if m.textOverlay == nil {
			log.ErrorLog.Printf("text overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.textOverlay.Render(), mainView, true, true)
	} else if m.state == stateConfirm {
		if m.confirmationOverlay == nil {
			log.ErrorLog.Printf("confirmation overlay is nil")
		}
		return overlay.PlaceOverlay(0, 0, m.confirmationOverlay.Render(), mainView, true, true)
	} else if m.state == stateHostSelect {
		if m.hostSelector == nil {
			log.ErrorLog.Printf("host selector is nil")
			return mainView
		}
		return overlay.PlaceOverlay(0, 0, m.hostSelector.Render(), mainView, true, true)
	} else if m.state == stateRepoSelect {
		if m.repoSelector == nil {
			log.ErrorLog.Printf("repo selector is nil")
			return mainView
		}
		return overlay.PlaceOverlay(0, 0, m.repoSelector.Render(), mainView, true, true)
	} else if m.state == statePresetSelect {
		if m.presetSelector == nil {
			log.ErrorLog.Printf("preset selector is nil")
			return mainView
		}
		return overlay.PlaceOverlay(0, 0, m.presetSelector.Render(), mainView, true, true)
	}

	return mainView
}

// pinOrchestratorsFirst performs a stable partition of the loaded instances:
// all KindOrchestrator instances come first, then all workers, with the
// relative order within each group preserved. This guarantees the
// orchestrator (cs2's "instance 0") is at the head of the list on cs2 open,
// so the default selection (index 0) lands on it and the user can interact
// with it immediately. A simple two-slice split+concat is stable by
// construction and avoids pulling in sort.Slice (whose stability is not
// guaranteed for the zero-struct comparator we'd otherwise need).
func pinOrchestratorsFirst(instances []*session.Instance) []*session.Instance {
	if len(instances) <= 1 {
		return instances
	}
	orchs := make([]*session.Instance, 0, len(instances))
	workers := make([]*session.Instance, 0, len(instances))
	for _, in := range instances {
		if in.Kind() == session.KindOrchestrator {
			orchs = append(orchs, in)
		} else {
			workers = append(workers, in)
		}
	}
	return append(orchs, workers...)
}
