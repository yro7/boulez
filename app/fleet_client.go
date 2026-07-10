package app

import (
	"encoding/json"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/yro7/boulez/kernel"
	"github.com/yro7/boulez/log"
	"github.com/yro7/boulez/session"
)

// fleetClient is the TUI's seam over the daemon's control socket. The TUI is
// a pure client of the kernel: it owns the VIEW (a read-only cache of the
// fleet), not the TRUTH. Every fleet mutation goes through this seam, and the
// daemon's kernel is the single writer. The TUI has no fleet persistence of
// its own — storage write methods live unexported on kernel.Storage (C4.3), so
// app/ cannot reach them even at compile time.
//
// Mirrors app/land_caller.go's shape: a thin adapter that speaks the wire
// protocol so the TUI neither imports nor constructs a *kernel.Kernel. One
// seam, one file.
//
// Option B of the inversion plan: ListInstances returns full InstanceData
// records (via the `list_instances_full` syscall) so the TUI can reconstruct
// read-only *session.Instance view handles through session.FromInstanceData.
// This preserves the TUI's read paths (preview/diff/terminal panes, attach,
// push) that need a worktree path, without the TUI writing fleet state. The
// reconstructed handles share tmux session names + worktree paths with the
// kernel's live instances, so direct reads (Preview, ComputeDiff, Attach)
// operate on the same underlying tmux sessions the daemon owns.
//
// Tests inject a fake fleetClient; production wires newSocketFleetClient().
type fleetClient interface {
	// ListInstances returns the full serializable fleet. The TUI reconciles
	// its read-only cache against this snapshot.
	ListInstances() ([]session.InstanceData, error)
	// Spawn creates and starts an instance via the kernel; returns the new ID.
	Spawn(opts SpawnOptions) (string, error)
	// Pause / Resume / Kill route the mutating keybindings through the kernel
	// so the single-writer invariant holds (the TUI no longer touches
	// inst.Pause/Resume/Kill directly for fleet-state mutations).
	Pause(id string) error
	Resume(id string) error
	Kill(id string) error
	// Archive soft-deletes an instance (kills tmux, keeps worktree+branch)
	// so it can be restored within the retention window.
	Archive(id string) error
	// Restore un-archives a soft-deleted instance (recreates tmux, back to Ready).
	Restore(id string) error
}

// socketFleetClient is the production fleetClient backed by the daemon's
// control socket.
type socketFleetClient struct{}

// newSocketFleetClient returns the production fleetClient.
func newSocketFleetClient() fleetClient {
	return socketFleetClient{}
}

func socketPath() (string, error) {
	p, err := kernel.SocketPath()
	if err != nil {
		return "", fmt.Errorf("fleet: resolve socket: %w", err)
	}
	return p, nil
}

func callFleet(method string, params any) (kernel.Response, error) {
	p, err := socketPath()
	if err != nil {
		return kernel.Response{}, err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return kernel.Response{}, fmt.Errorf("fleet: marshal params: %w", err)
	}
	resp, err := kernel.Call(p, kernel.Request{Method: method, Params: raw})
	if err != nil {
		return kernel.Response{}, fmt.Errorf("fleet: %s: %w (is the daemon running?)", method, err)
	}
	if resp.Error != nil {
		return resp, fmt.Errorf("fleet: %s: %s: %s", method, resp.Error.Code, resp.Error.Message)
	}
	return resp, nil
}

// ListInstances fetches the full fleet as InstanceData via the
// `list_instances_full` syscall.
func (socketFleetClient) ListInstances() ([]session.InstanceData, error) {
	resp, err := callFleet("list_instances_full", map[string]interface{}{})
	if err != nil {
		return nil, err
	}
	var out []session.InstanceData
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		return nil, fmt.Errorf("fleet: decode list_instances_full: %w", err)
	}
	return out, nil
}

// Spawn issues a `spawn_worker` syscall mirroring app.SpawnOptions. The kernel
// creates+starts the instance and returns its ID. Host is carried by alias
// (host.Lookup resolves it on the daemon side).
//
// The wire payload is a kernel.SpawnParams built from app.SpawnOptions, so the
// JSON field names (and their omitempty rules) live in exactly one place —
// the SpawnParams struct tags in kernel/transport.go. The app no longer
// hand-writes a map[string]interface{} for spawn, which was silently
// drop-prone on any field rename.
func (socketFleetClient) Spawn(opts SpawnOptions) (string, error) {
	params := kernel.SpawnParams{
		Repo:            opts.Repo,
		Branch:          opts.Branch,
		BranchMustExist: opts.BranchMustExist,
		Prompt:          opts.Prompt,
		Program:         opts.Program,
		Title:           opts.Title,
		Kind:            opts.Kind,
	}
	if opts.Host != nil {
		params.Host = opts.Host.Name()
	}
	resp, err := callFleet("spawn_worker", params)
	if err != nil {
		return "", err
	}
	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		return "", fmt.Errorf("fleet: decode spawn_worker: %w", err)
	}
	return res.ID, nil
}

func (socketFleetClient) Pause(id string) error {
	_, err := callFleet("pause", map[string]interface{}{"id": id})
	return err
}

func (socketFleetClient) Resume(id string) error {
	_, err := callFleet("resume", map[string]interface{}{"id": id})
	return err
}

func (socketFleetClient) Kill(id string) error {
	_, err := callFleet("kill", map[string]interface{}{"id": id})
	return err
}

func (socketFleetClient) Archive(id string) error {
	_, err := callFleet("archive", map[string]interface{}{"id": id})
	return err
}

func (socketFleetClient) Restore(id string) error {
	_, err := callFleet("restore", map[string]interface{}{"id": id})
	return err
}

// Compile-time check that socketFleetClient satisfies the seam.
var _ fleetClient = socketFleetClient{}

// resolveFleet returns the home's fleet client, defaulting to the socket-backed
// production client when nil (so a bare &home{} test construct still works).
// Mirrors the landCaller lazy-default pattern.
func (m *home) resolveFleet() fleetClient {
	if m.fleet == nil {
		m.fleet = newSocketFleetClient()
	}
	return m.fleet
}

// fleetTickMsg triggers a fleet refresh from the kernel on a steady cadence
// (C3.2). The TUI polls list_instances_full at human cadence so external
// mutations (boulez ctl, another TUI, an orchestrator) become visible without a
// per-keystroke round-trip. Mutations the TUI itself issues refresh the fleet
// immediately on their ack (see refreshFleetAfterMutation).
type fleetTickMsg struct{}

const fleetTickInterval = 1 * time.Second

// refreshFleetFromKernel fetches the fleet snapshot from the kernel and
// reconciles the TUI's read-only cache against it (C3.2). This is the TUI's
// only read path for fleet membership: the kernel is the single writer, the
// TUI owns the view. Existing cached instances are kept by ID (so the
// background metadata tick's pointers stay valid and tmux bindings are not
// needlessly re-Restored); only their lightweight state (Status, AutoYes) is
// updated in place. New instances are reconstructed via session.FromInstanceData
// so the TUI gets a worktree-backed view handle for the preview/diff/terminal
// panes and attach. Instances absent from the snapshot are dropped.
//
// Orchestrators are pinned to the front of the view (a view-only concern;
// the kernel's own ordering is insertion order).
func (m *home) refreshFleetFromKernel() error {
	data, err := m.resolveFleet().ListInstances()
	if err != nil {
		return err
	}
	m.reconcileFleet(data)
	return nil
}

// refreshFleetAfterMutation refreshes the fleet after the TUI issues a
// mutation syscall (spawn/pause/resume/kill). Errors are surfaced via the
// error box rather than fatal — the mutation itself already succeeded; a
// refresh failure only means the view is briefly stale (the next fleet tick
// reconciles). Returns a tea.Cmd so callers can batch it.
func (m *home) refreshFleetAfterMutation() tea.Cmd {
	if err := m.refreshFleetFromKernel(); err != nil {
		return m.handleError(err)
	}
	return m.instanceChanged()
}

// reconcileFleet applies a kernel fleet snapshot to the TUI's view. See
// refreshFleetFromKernel for the contract.
func (m *home) reconcileFleet(data []session.InstanceData) {
	existing := m.list.GetInstances()
	byID := make(map[string]*session.Instance, len(existing))
	for _, inst := range existing {
		byID[inst.GetID()] = inst
	}

	seen := make(map[string]struct{}, len(data))
	out := make([]*session.Instance, 0, len(data))
	for _, d := range data {
		seen[d.ID] = struct{}{}
		if inst, ok := byID[d.ID]; ok {
			// Reuse the existing view handle: keep its tmux/worktree binding,
			// only refresh the lightweight state the kernel owns. The status
			// is the daemon's authority (it observes + stabilizes + pushes via
			// k.UpdateStatus); the TUI only renders it.
			inst.Status = d.Status
			inst.AutoYes = d.AutoYes
			// Render-only side effects of the Ready transition, formerly done in
			// the metadataUpdateDoneMsg handler. The agent finished a turn after
			// resuming work (Running → Ready): the displayed version no longer
			// matches what was last landed, so clear the TUI-only landed hint
			// (the dimmed row + checkmark disappears). Notify when configured.
			// This is pure view state (the kernel's own Landed flag, set on Land,
			// is untouched) — same split as before the refactor.
			//
			// prev is compared against the last KERNEL-reported status
			// (m.prevStatus), not inst.Status: a freshly-reconstructed view
			// handle (FromInstanceData → Start) has its status forced to Running
			// by Start regardless of d.Status, so inst.Status is unreliable right
			// after reconstruction and would produce a spurious transition.
			if last, ok := m.prevStatus[d.ID]; ok && last != session.Ready && d.Status == session.Ready {
				inst.SetLanded(false)
				if m.appConfig.NotifyOnReady {
					m.notifyReady(inst)
				}
			}
			m.prevStatus[d.ID] = d.Status
			out = append(out, inst)
			continue
		}
		// New instance: reconstruct a read-only view handle. FromInstanceData
		// restores the tmux binding for live instances (so preview/attach
		// work) and sets up the worktree path for the terminal/diff panes. A
		// reconstruction failure (e.g. a transiently-unreachable tmux session,
		// Bug B territory) is logged and skipped so one bad instance does not
		// blank the whole view.
		inst, err := session.FromInstanceData(d)
		if err != nil {
			log.ErrorLog.Printf("fleet: could not reconstruct instance %s (%s): %v", d.ID, d.Title, err)
			continue
		}
		// Seed the last-seen kernel status so the next tick can detect a
		// real transition (no side effects on the first appearance: the
		// instance is brand new to the view, there is no prior state).
		if m.prevStatus == nil {
			m.prevStatus = make(map[string]session.Status)
		}
		m.prevStatus[d.ID] = d.Status
		out = append(out, inst)
	}

	// Preserve TUI-local drafts (name entry / spawn in-flight) that the
	// kernel does not yet know about. Without this, the periodic fleet tick
	// would drop the draft mid-name-entry and the stateNew handler would
	// panic on the next keypress (index out of range). Drafts are appended
	// after the kernel's instances; pinOrchestratorsFirst below moves any
	// orchestrator draft to the front so the view ordering stays stable.
	for _, inst := range existing {
		if _, seen := seen[inst.GetID()]; seen {
			continue
		}
		if _, draft := m.pendingDraftIDs[inst.GetID()]; draft {
			out = append(out, inst)
		}
	}

	// Pin orchestrators to the front of the view (stable partition). This is a
	// view-only concern: the kernel's ordering is insertion order, but the
	// TUI's default selection is index 0, so the orchestrator must be first.
	out = pinOrchestratorsFirst(out)

	// Prune transition-tracking state for instances no longer in the kernel's
	// snapshot so the map does not leak an entry per ever-seen instance ID.
	for id := range m.prevStatus {
		if _, ok := seen[id]; !ok {
			delete(m.prevStatus, id)
		}
	}

	m.list.SetInstances(out)
}

// --- spawn routing (C3.3) ---

// trackDraft marks an instance ID as a TUI-local draft: held in the list
// during name entry or while a spawn syscall is in flight, but not yet
// known to the kernel. reconcileFleet preserves tracked drafts against the
// fleet snapshot so the periodic fleet tick does not wipe a name-entry
// draft (which would panic the stateNew handler).
func (m *home) trackDraft(id string) {
	if m.pendingDraftIDs == nil {
		m.pendingDraftIDs = make(map[string]struct{})
	}
	m.pendingDraftIDs[id] = struct{}{}
}

// untrackDraft removes an ID from the pending-draft set. Idempotent. Called
// when a draft leaves the list: spawn ack (success or error) or name-entry
// cancel.
func (m *home) untrackDraft(id string) {
	delete(m.pendingDraftIDs, id)
}

// spawnDoneMsg is sent when a fleet.Spawn syscall completes (C3.3). The TUI
// routes spawn through the kernel (single writer); the syscall returns the new
// ID and the TUI re-reads the fleet (C3.2) to pick it up. The draftID is the
// TUI-local draft instance kept in the list during name entry — on ack it is
// removed because the kernel now owns the real instance (with its own ID).
type spawnDoneMsg struct {
	id           string // new instance ID (empty on error)
	title        string // requested title (for the help screen + fallback selection)
	err          error
	draftID      string // TUI-local draft to remove on ack
	orchestrator bool   // run the orchestrator post-spawn injection on success
}

// runSpawnCmd issues a spawn_worker syscall in the background and returns the
// result as spawnDoneMsg. The draft instance stays in the list (showing
// Loading) while the kernel creates+starts the real instance.
func (m *home) runSpawnCmd(opts SpawnOptions, draftID string, orchestrator bool) tea.Cmd {
	return func() tea.Msg {
		id, err := m.resolveFleet().Spawn(opts)
		return spawnDoneMsg{
			id:           id,
			title:        opts.Title,
			err:          err,
			draftID:      draftID,
			orchestrator: orchestrator,
		}
	}
}
