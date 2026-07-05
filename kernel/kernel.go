// Package kernel is the control authority of boulez: the single writer that
// owns the fleet's mutable state and exposes the orchestration syscalls as
// pure Go methods.
//
// The kernel is the OS metaphor: it is the long-running process that owns
// the instances (processes) and applies guards of security that no client
// can bypass. The TUI is a console (observer); `boulez ctl` is a thin client
// that sends requests to the kernel via a transport (step 6). The kernel
// itself knows nothing of transports or LLMs — it is consumer-agnostic, in
// the same spirit as program.Adapter.
//
// Testability is the core property: every syscall is a method on Kernel
// backed by injectable interfaces (a Spawner and a git.Merger). Tests drive
// the kernel with fakes — no tmux, no socket, no real agent. This is the
// deep-module move: a small surface (8 syscalls) hiding the entire fleet's
// lifecycle and the merge machinery.
package kernel

import (
	"fmt"
	"github.com/yro7/boulez/host"
	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/session/git"
	"sync"
	"time"
)

// SpawnOptions mirrors app.SpawnOptions but lives in the kernel layer to
// avoid importing app (which pulls in the TUI). The kernel translates a
// syscall request into these options. Keeping a parallel struct also makes
// the kernel's dependency on "how to create an instance" explicit and
// swappable — the Spawner seam.
type SpawnOptions struct {
	Repo            string
	Branch          string
	BranchMustExist bool
	Prompt          string
	Program         string
	Title           string
	Host            host.Host
	Kind            session.Kind
}

// Spawner is the seam for creating+starting an instance. The real
// implementation is app.Spawn (which wires tmux); tests inject a fake that
// returns an in-memory instance without touching tmux. This is what makes
// the kernel testable without a PTY: the only tmux-coupled operation
// (instance creation+start) is behind this interface.
type Spawner interface {
	Spawn(opts SpawnOptions) (*session.Instance, error)
}

// CallerContext identifies who is issuing a syscall. Today it carries the
// caller's Kind so the recursion guard (a Worker cannot spawn) can be
// enforced. When the transport (step 6) authenticates a control session to
// an orchestrator instance, it builds a CallerContext from that instance.
// v1 callers pass CallerContext{} (empty CallerID = top-level `boulez ctl`) for
// top-level control, which is NOT subject to the Worker guard.
type CallerContext struct {
	// CallerID is the instance ID of the caller. Empty = top-level `boulez ctl`
	// (no instance caller), which is allowed to spawn any Kind.
	CallerID string
	// Kind is the caller's Kind. Only meaningful when CallerID is non-empty.
	Kind session.Kind
}

// IsTopLevel reports whether the caller is `boulez ctl` itself (no instance
// caller). A top-level caller is never subject to the Worker guard.
func (c CallerContext) IsTopLevel() bool {
	return c.CallerID == ""
}

// IsWorker reports whether the caller is a Worker instance (and thus barred
// from spawning). A top-level caller is not a Worker.
func (c CallerContext) IsWorker() bool {
	if c.IsTopLevel() {
		return false
	}
	return c.Kind == session.KindWorker
}

// Kernel is the single writer that owns the fleet. It holds the in-memory
// instance set, the storage backend, and the merge machinery. All mutating
// syscalls go through the kernel so the guards are enforced in one place.
type Kernel struct {
	mu       sync.Mutex
	storage  *Storage
	spawner  Spawner
	merger   git.Merger
	autosave bool // if true, persist to storage after every mutation

	// instStore is the in-memory fleet. Loaded lazily from storage on first
	// access. Owned by the kernel (single writer).
	instStore *instances

	// protectedBranches is the kernel-level set of branch names a merge may
	// never target, beyond the conventional main/master the Merger already
	// refuses (defense in depth lives there). It carries the protected store's
	// union (spec decision 7): the daemon has no cwd, so protected branches
	// are declared explicitly per repo in ~/.boulez/protected.json and fed here at
	// boot; the daemon hot-swaps the set on SIGHUP via SetProtectedBranches.
	// The Merger cannot know the host repo, so this guard lives in the kernel
	// — the authority that applies guards no client can bypass.
	protectedBranches []string

	// sessions tracks authenticated control connections by session id. Each
	// session binds a connection to an instance identity (via `authenticate`),
	// so syscalls are attributed to the right caller for the recursion guards.
	// Unauthenticated (top-level) sessions aren't tracked here — they're
	// stateless. Guarded by k.mu.
	sessions map[string]*ctlSession
}

// Option configures a Kernel.
type Option func(*Kernel)

// WithSpawner injects the instance spawner. Tests pass a fake; production
// passes the real app.Spawn-backed spawner.
func WithSpawner(s Spawner) Option {
	return func(k *Kernel) { k.spawner = s }
}

// WithMerger injects the merge machinery. Tests pass a fake; production
// passes git.NewMerger.
func WithMerger(m git.Merger) Option {
	return func(k *Kernel) { k.merger = m }
}

// WithoutAutosave disables persistence after each mutation. Tests use this
// to keep the kernel pure (no disk writes) and inspect in-memory state.
func WithoutAutosave() Option {
	return func(k *Kernel) { k.autosave = false }
}

// WithProtectedBranches injects the kernel-level protected-branch set: branch
// names a merge may never target, on top of the conventional main/master the
// Merger already refuses. The daemon passes the protected store's union here
// so an orchestrator cannot merge into a declared-protected branch (spec
// decision 7, non-contournable by the client).
func WithProtectedBranches(branches []string) Option {
	return func(k *Kernel) { k.protectedBranches = append([]string(nil), branches...) }
}

// SetProtectedBranches replaces the kernel-level protected-branch set at
// runtime. It is the SIGHUP reload contract (C2.2): the daemon re-reads the
// protected store on SIGHUP and pushes the new union here, without
// reconstructing the kernel (the single-writer invariant holds). Safe for
// concurrent use with Merge/Land, which snapshot the set under k.mu.
func (k *Kernel) SetProtectedBranches(branches []string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.protectedBranches = append([]string(nil), branches...)
}

// New builds a Kernel over the given storage. The spawner defaults to a
// no-op spawner that errors (production wires the real one via WithSpawner);
// the merger defaults to git.NewMerger(nil) (local executor). Autosave is on
// by default so production persists every mutation.
func New(storage *Storage, opts ...Option) *Kernel {
	k := &Kernel{
		storage:  storage,
		merger:   git.NewMerger(nil),
		autosave: true,
		spawner:  erroringSpawner{},
	}
	for _, opt := range opts {
		opt(k)
	}
	return k
}

// erroringSpawner is the default: it refuses to spawn because no real
// spawner was wired. This makes a misconfigured kernel fail loudly rather
// than silently no-op-ing.
type erroringSpawner struct{}

func (erroringSpawner) Spawn(SpawnOptions) (*session.Instance, error) {
	return nil, fmt.Errorf("kernel: no spawner wired (use WithSpawner)")
}

// --- syscalls ---

// ListInstances returns a snapshot of the fleet, optionally filtered by
// Kind, repo, or status. Read syscall — safe to call concurrently.
func (k *Kernel) ListInstances(filter ListFilter) []InstanceSummary {
	k.mu.Lock()
	defer k.mu.Unlock()

	instances := k.instancesLocked()
	out := make([]InstanceSummary, 0, len(instances))
	for _, inst := range instances {
		s := summarize(inst)
		if !filter.matches(s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// ListInstancesData returns a snapshot of the fleet as serializable
// InstanceData records, optionally filtered. It is the TUI's read path: the
// TUI is a pure client of the kernel and reconstructs read-only view handles
// (session.FromInstanceData) from these records. Returning InstanceData (not
// just InstanceSummary) lets the TUI rebuild worktree-backed handles for the
// preview/diff/terminal panes and attach, without the TUI reading
// session.Storage or writing fleet state. The kernel remains the single
// writer; this is a read syscall, safe to call concurrently.
//
// This is Option B of the inversion plan: the TUI keeps a read-only cache of
// *session.Instance view handles (refreshed from this syscall), while every
// fleet mutation goes through the kernel. The wire alias is
// `list_instances_full`; the lightweight `list_instances` (summaries) stays
// for `boulez ctl`'s human-facing output.
func (k *Kernel) ListInstancesData(filter ListFilter) []session.InstanceData {
	k.mu.Lock()
	defer k.mu.Unlock()

	instances := k.instancesLocked()
	out := make([]session.InstanceData, 0, len(instances))
	for _, inst := range instances {
		s := summarize(inst)
		if !filter.matches(s) {
			continue
		}
		out = append(out, inst.ToInstanceData())
	}
	return out
}

// GetInstance returns the details of a single instance by ID, including its
// diff and tmux scrollback (best-effort). Read syscall.
func (k *Kernel) GetInstance(id string) (InstanceDetail, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	inst, ok := k.findLocked(id)
	if !ok {
		return InstanceDetail{}, ErrUnknownInstance{ID: id}
	}
	return detail(inst), nil
}

// Spawn creates and starts a new instance. The recursion guard refuses if the
// caller is a Worker (topology is strictly two levels in v1). Returns the new
// instance's ID. Mutating syscall.
func (k *Kernel) Spawn(caller CallerContext, opts SpawnOptions) (string, error) {
	if caller.IsWorker() {
		return "", ErrWorkerCannotSpawn{}
	}
	if !caller.IsTopLevel() && caller.Kind == session.KindOrchestrator && opts.Kind == session.KindOrchestrator {
		// v1: orchestrators cannot spawn orchestrators (two-level topology).
		// Lifting this is a future extension point (super-orchestrator).
		return "", ErrNestedOrchestrator{}
	}

	k.mu.Lock()
	if k.spawner == nil {
		k.mu.Unlock()
		return "", fmt.Errorf("kernel: no spawner wired")
	}
	spawner := k.spawner
	k.mu.Unlock()

	// Spawn outside the kernel lock: instance start touches tmux/git and can
	// be slow; we don't want to block other syscalls. The kernel re-locks to
	// register the result.
	inst, err := spawner.Spawn(opts)
	if err != nil {
		return "", fmt.Errorf("spawn: %w", err)
	}

	k.mu.Lock()
	k.instancesLocked() // load if not yet loaded
	k.registerLocked(inst)
	autosave := k.autosave
	storage := k.storage
	k.mu.Unlock()

	// If an orchestrator spawned this worker, record it in the orchestrator's
	// plan (resumability substrate). Best-effort: a plan-save failure does not
	// abort a successful spawn.
	if !caller.IsTopLevel() && caller.Kind == session.KindOrchestrator && opts.Kind == session.KindWorker {
		_ = recordWorkerInPlan(caller.CallerID, inst.GetID())
	}

	if autosave && storage != nil {
		_ = k.persist(storage, inst)
	}
	return inst.GetID(), nil
}

// SendPrompt sends a prompt to an instance by ID. Mutating syscall.
func (k *Kernel) SendPrompt(id, prompt string) error {
	k.mu.Lock()
	inst, ok := k.findLocked(id)
	k.mu.Unlock()
	if !ok {
		return ErrUnknownInstance{ID: id}
	}
	return inst.SendPrompt(prompt)
}

// Pause pauses an instance by ID. Mutating syscall.
func (k *Kernel) Pause(id string) error {
	k.mu.Lock()
	inst, ok := k.findLocked(id)
	storage := k.storage
	autosave := k.autosave
	k.mu.Unlock()
	if !ok {
		return ErrUnknownInstance{ID: id}
	}
	if err := inst.Pause(); err != nil {
		return err
	}
	if autosave && storage != nil {
		_ = k.persist(storage, inst)
	}
	return nil
}

// Resume resumes a paused instance by ID. Mutating syscall.
func (k *Kernel) Resume(id string) error {
	k.mu.Lock()
	inst, ok := k.findLocked(id)
	storage := k.storage
	autosave := k.autosave
	k.mu.Unlock()
	if !ok {
		return ErrUnknownInstance{ID: id}
	}
	if err := inst.Resume(); err != nil {
		return err
	}
	if autosave && storage != nil {
		_ = k.persist(storage, inst)
	}
	return nil
}

// Kill terminates an instance by ID and removes it from the fleet. Mutating.
//
// The record is ALWAYS removed from the in-memory fleet and persisted, even
// when the underlying resource cleanup (tmux session, git worktree, branch)
// partially fails. This is the C4.1 invariant: a killed instance must never
// linger as an unkillable zombie. If we returned early on a cleanup error the
// record would stay in the fleet forever (the regression seen when killing
// an already-Dead instance whose tmux session is gone — inst.Kill errors on
// kill-session, and the record was never removed). Cleanup errors are
// collected and returned to the caller as a single error AFTER the removal
// and persistence have happened, so the user sees what failed but the fleet
// stays consistent. A nil error means everything cleaned up cleanly.
func (k *Kernel) Kill(id string) error {
	k.mu.Lock()
	inst, ok := k.findLocked(id)
	storage := k.storage
	autosave := k.autosave
	k.mu.Unlock()
	if !ok {
		return ErrUnknownInstance{ID: id}
	}

	// Best-effort resource cleanup. Errors here are surfaced to the caller
	// but do NOT prevent the record removal below — a half-cleaned instance
	// (tmux session already gone, worktree dir missing) is still removed
	// from the fleet.
	killErr := inst.Kill()

	// Remove from the in-memory fleet before persisting. Without this the
	// just-killed instance is re-saved to storage and resurrected on the next
	// daemon boot (Bug B: kill zombie). The kernel is the single writer, so
	// the store is mutated only here, under the lock.
	k.mu.Lock()
	if k.instStore != nil {
		k.instStore.remove(id)
	}
	k.mu.Unlock()
	if autosave && storage != nil {
		_ = k.persist(storage, inst)
	}
	return killErr
}

// Merge merges source branches into a target branch of a repo. The guarded
// syscall: the kernel delegates to the Merger (which itself refuses protected
// branches), records the outcome, and updates the caller's plan. v1 does NOT
// auto-resolve conflicts — a conflict returns MergeConflict and the caller
// (an orchestrator, Shape B) decides to spawn a resolver. Mutating.
func (k *Kernel) Merge(caller CallerContext, repoPath, targetBranch string, sourceBranches []string, strategy git.Strategy) (git.MergeResult, error) {
	// Kernel-level guard (spec decision 7, non-contournable): refuse to merge
	// into any declared-protected branch (the protected store's union). The
	// Merger defends main/master in depth separately; this guard covers the
	// declared set the Merger cannot see. Snapshot under k.mu so a concurrent
	// SIGHUP reload (SetProtectedBranches) cannot race the read.
	k.mu.Lock()
	protected := append([]string(nil), k.protectedBranches...)
	merger := k.merger
	k.mu.Unlock()
	if isKernelProtected(protected, targetBranch) {
		return git.MergeResult{Status: git.MergeConflict, Message: "protected branch"}, git.ErrProtectedBranch{Branch: targetBranch}
	}

	if merger == nil {
		return git.MergeResult{}, fmt.Errorf("kernel: no merger wired")
	}

	// Record the merge intent on the caller's plan (if the caller is an
	// orchestrator). Best-effort.
	if !caller.IsTopLevel() && caller.Kind == session.KindOrchestrator {
		_ = RecordMerge(caller.CallerID, MergeTarget{Repo: repoPath, Branch: targetBranch, Sources: sourceBranches})
	}

	res, err := merger.Merge(repoPath, targetBranch, sourceBranches, strategy)
	if err == nil && !caller.IsTopLevel() && caller.Kind == session.KindOrchestrator {
		_ = recordMergeOutcome(caller.CallerID, res)
	}
	// Persist + propagate the landed state for the merged sources: every
	// instance whose branch was a source of a successful merge is marked
	// landed so `boulez ctl list_instances` / `get_instance` reflect it. A
	// conflict does NOT mark landed (the branch did not actually land).
	if err == nil && res.Status != git.MergeConflict {
		k.markSourcesLanded(sourceBranches)
	}
	return res, err
}

// LandResult is the outcome of the Land syscall. It carries the git merge
// outcome plus host-sync hints surfaced to the TUI: whether the host repo's
// working tree was fast-pathed to the merged main (HostSynced), and if not,
// a human-readable recovery hint (HostSyncNote). This is what the wire
// marshals and what session.LandOutcome mirrors on the seam side.
type LandResult struct {
	Merge        git.MergeResult
	HostSynced   bool
	HostSyncNote string
}

// Land merges a single source branch into a target branch of a repo, with the
// explicit authority to land onto a trunk (main/master). This is the "land to
// main" syscall: it bypasses ONLY the conventional-trunk guard, and only for a
// top-level caller. Workers and orchestrators cannot call it (they must use
// Merge, which refuses trunks). The protected-branch guard still applies:
// you cannot land into a declared-protected branch.
//
// The merge runs in two phases:
//  1. Fast path: if the host repo has targetBranch checked out, a clean
//     working tree, and sourceBranch can fast-forward targetBranch, the merge
//     runs in place (exactly like `git pull --ff-only`). index + worktree
//     advance to the merged commit and the host can build from main
//     immediately. No `git reset --hard` is ever used; git itself validates
//     the losslessness (ff-only refuses non-ff).
//  2. Fallback: otherwise (host on another branch / non-ff / race — the
//     fast-path refused), the throwaway-worktree merger runs the merge in an
//     isolated worktree, then advances the target ref via update-ref. The
//     host worktree is then synced IF AND ONLY IF it is on targetBranch and
//     CLEAN (status --porcelain empty): a lossless `git reset --hard` brings
//     index + worktree to the new ref so the user can build from main
//     immediately, exactly like the fast path. If the host is on targetBranch
//     but DIRTY, the fallback REFUSES (git.ErrHostOnTargetBranchDirty) and
//     mutates nothing — `reset --hard` would lose uncommitted tracked work,
//     and a bare update-ref would diverge HEAD from the worktree (the
//     regression where every touched file reads as "modified" and `git pull
//     --ff-only` fails). When the host is on ANOTHER branch, update-ref is
//     safe (no divergence possible) and the host worktree is left untouched
//     with a recovery hint.
//
// v1 lands ONE source branch per call (the instance's own branch). On
// conflict, MergeConflict is returned and the repo is left for resolution
// (no silent --abort). There is no plan to update (a top-level caller has no
// plan) and no RecordMerge (reserved for orchestrators).
func (k *Kernel) Land(caller CallerContext, repoPath, targetBranch, sourceBranch string, strategy git.Strategy) (LandResult, error) {
	// Topology guard: only a top-level caller may land onto a trunk. This is
	// the mirror of the Worker recursion guard — the v1 topology forbids
	// instances (workers or orchestrators) from touching the trunk.
	if !caller.IsTopLevel() {
		return LandResult{Merge: git.MergeResult{Status: git.MergeConflict, Message: "non-top-level land"}}, ErrNonTopLevelLand{}
	}

	// Kernel-level guard (spec decision 7): refuse to land into a
	// declared-protected branch. The Merger's trunk guard is intentionally
	// lifted for this path (via MergeTrunk), but the protected-branch guard
	// is NOT — landing into a protected branch would clobber the user's working
	// tree. Snapshot under k.mu so a concurrent SIGHUP reload cannot race.
	k.mu.Lock()
	protected := append([]string(nil), k.protectedBranches...)
	merger := k.merger
	k.mu.Unlock()
	if isKernelProtected(protected, targetBranch) {
		return LandResult{Merge: git.MergeResult{Status: git.MergeConflict, Message: "protected branch"}}, git.ErrProtectedBranch{Branch: targetBranch}
	}

	if merger == nil {
		return LandResult{}, fmt.Errorf("kernel: no merger wired")
	}

	// Phase 1 — fast path: ff-only merge in place. Lossless (git validates),
	// and syncs the host working tree so the user can build from main right
	// away. Only attempted when the merger exposes the in-place variant.
	if inPlace, ok := merger.(git.MergerInPlace); ok {
		handled, res, err := inPlace.MergeTrunkInPlace(repoPath, targetBranch, []string{sourceBranch}, strategy)
		if handled {
			if err != nil {
				return LandResult{Merge: res, HostSynced: false, HostSyncNote: "in-place ff-only merge failed"}, err
			}
			result := LandResult{Merge: res, HostSynced: res.Status == git.MergeMerged, HostSyncNote: ""}
			if res.Status != git.MergeConflict {
				k.markSourcesLanded([]string{sourceBranch})
			}
			return result, nil
		}
		// handled=false: fast path did not apply (host on another branch /
		// dirty / non-ff). Fall through to the throwaway merger and report a
		// recovery hint so the user knows the host worktree was not synced.
	}

	// Phase 2 — fallback: throwaway-worktree merge. The ref advances via
	// update-ref but the host working tree is NOT touched. Surface a clear
	// recovery hint instead of leaving the user wondering why `go build` from
	// main still builds the old code.
	merge, err := merger.MergeTrunk(repoPath, targetBranch, []string{sourceBranch}, strategy)
	note := fmt.Sprintf("host not synced — run: git -C %s pull --ff-only", repoPath)
	if err == nil && merge.Status != git.MergeConflict {
		k.markSourcesLanded([]string{sourceBranch})
	}
	return LandResult{Merge: merge, HostSynced: false, HostSyncNote: note}, err
}

// markSourcesLanded marks every instance whose branch is in sourceBranches as
// landed and refreshes its UpdatedAt, persisting the fleet if autosave is on.
// Called by Merge/Land after a successful (non-conflict) merge so the fleet
// reflects that the instance's branch has been merged into the target trunk —
// the persisted, wire-visible state `boulez ctl list_instances` / `get_instance`
// expose. The TUI's own SetLanded(true) on a landDoneMsg coexists with this:
// the TUI operates on read-only view handles and is not the single writer, so
// its calls do not persist; this kernel path is what makes the state survive
// a daemon restart and reach non-TUI consumers.
//
// Caller must NOT hold k.mu.
func (k *Kernel) markSourcesLanded(sourceBranches []string) {
	if len(sourceBranches) == 0 {
		return
	}
	k.mu.Lock()
	instances := k.instancesLocked()
	marked := false
	now := time.Now()
	for _, inst := range instances {
		if !inst.Started() {
			continue
		}
		for _, b := range sourceBranches {
			if inst.Branch == b {
				inst.SetLanded(true)
				inst.UpdatedAt = now
				marked = true
				break
			}
		}
	}
	storage := k.storage
	autosave := k.autosave
	k.mu.Unlock()
	if marked && autosave && storage != nil {
		_ = k.persist(storage, nil)
	}
}
