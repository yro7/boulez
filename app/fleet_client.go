package app

import (
	"encoding/json"
	"fmt"
	"time"

	"claude-squad/kernel"
	"claude-squad/log"
	"claude-squad/session"
	tea "github.com/charmbracelet/bubbletea"
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

func callFleet(method string, params map[string]interface{}) (kernel.Response, error) {
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
func (socketFleetClient) Spawn(opts SpawnOptions) (string, error) {
	params := map[string]interface{}{
		"repo":    opts.Repo,
		"prompt":  opts.Prompt,
		"program": opts.Program,
		"title":   opts.Title,
	}
	if opts.Branch != "" {
		params["branch"] = opts.Branch
	}
	if opts.BranchMustExist {
		params["branch_must_exist"] = true
	}
	if opts.Kind != session.KindWorker {
		params["kind"] = opts.Kind
	}
	if opts.Host != nil {
		params["host"] = opts.Host.Name()
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
// mutations (cs2 ctl, another TUI, an orchestrator) become visible without a
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
			// only refresh the lightweight state the kernel owns.
			inst.Status = d.Status
			inst.AutoYes = d.AutoYes
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
		out = append(out, inst)
	}

	// Pin orchestrators to the front of the view (stable partition). This is a
	// view-only concern: the kernel's ordering is insertion order, but the
	// TUI's default selection is index 0, so the orchestrator must be first.
	out = pinOrchestratorsFirst(out)

	m.list.SetInstances(out)
}

// --- spawn routing (C3.3) ---

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
