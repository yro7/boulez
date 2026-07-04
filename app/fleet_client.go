package app

import (
	"encoding/json"
	"fmt"

	"claude-squad/kernel"
	"claude-squad/session"
)

// fleetClient is the TUI's seam over the daemon's control socket. The TUI is
// a pure client of the kernel: it owns the VIEW (a read-only cache of the
// fleet), not the TRUTH. Every fleet mutation goes through this seam, and the
// daemon's kernel is the single writer. The TUI never calls
// session.Storage.SaveInstances / LoadInstances.
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
