package session

import (
	"claude-squad/host"
	"claude-squad/log"
	"claude-squad/program"
	"claude-squad/session/git"
	"claude-squad/session/tmux"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/atotto/clipboard"
)

type Status int

const (
	// Running is the status when the instance is running and claude is working.
	Running Status = iota
	// Ready is if the claude instance is ready to be interacted with (waiting for user input).
	Ready
	// Loading is if the instance is loading (if we are starting it up or something).
	Loading
	// Paused is if the instance is paused (worktree removed but branch preserved).
	Paused
	// Dead is set when the kernel detects the instance's tmux session is
	// gone (e.g. after a daemon restart following a tmux crash). The instance
	// is kept visible so the user can inspect/kill it, but it is not running
	// and cannot be resumed or checked out — only killed. C4.4.
	Dead
)

// String renders the Status for logging and for the wire (JSON consumers
// see "running"/"ready"/"loading"/"paused" instead of opaque ints 0-3).
func (s Status) String() string {
	switch s {
	case Running:
		return "running"
	case Ready:
		return "ready"
	case Loading:
		return "loading"
	case Paused:
		return "paused"
	case Dead:
		return "dead"
	default:
		return "unknown"
	}
}

// MarshalJSON renders Status as a string on the wire, so 'list_instances'
// shows "status": "paused" rather than 3. Resolves finding #2 (enums as
// raw ints) and keeps the wire self-documenting for an LLM consumer.
func (s Status) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// UnmarshalJSON accepts EITHER a string ("running"/.../"paused") or an int
// (0-3) on the wire. The CLI status filter passes strings; raw JSON-RPC may
// pass either. Resolves finding #8 (string 'kind'/'status' rejected).
func (s *Status) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		switch strings.ToLower(str) {
		case "", "running":
			*s = Running
		case "ready":
			*s = Ready
		case "loading":
			*s = Loading
		case "paused":
			*s = Paused
		case "dead":
			*s = Dead
		default:
			return fmt.Errorf("invalid Status %q (want running|ready|loading|paused)", str)
		}
		return nil
	}
	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("Status must be a string or int: %w", err)
	}
	st := Status(n)
	switch st {
	case Running, Ready, Loading, Paused, Dead:
		*s = st
		return nil
	default:
		return fmt.Errorf("invalid Status int %d", n)
	}
}

// Kind classifies an instance's role. It is the point of extension for the
// orchestration hierarchy: today the topology is strictly two levels — a
// Worker cannot spawn, an Orchestrator can — but lifting that restriction
// later (super-orchestrator → n orchestrators → m workers) is a change to
// the spawn guard, not to the architecture. Persisted in InstanceData so it
// survives a restart.
type Kind int

const (
	// KindWorker is the default: an instance bound to a real git worktree,
	// editing code in isolation. This is the only Kind that existed before
	// the orchestrator work — back-compat default for legacy persisted data.
	KindWorker Kind = iota
	// KindOrchestrator is a supervising instance. It has no git worktree
	// (it does not edit code in a supervised repo); its worktree is the
	// headless no-op. It supervises the fleet via the control API.
	KindOrchestrator
)

// String renders the Kind for logging/debug.
func (k Kind) String() string {
	switch k {
	case KindWorker:
		return "worker"
	case KindOrchestrator:
		return "orchestrator"
	default:
		return "unknown"
	}
}

// MarshalJSON renders Kind as a human-readable string on the wire
// ("worker"/"orchestrator"), so a consumer parsing 'cs2 ctl list_instances'
// sees self-documenting values instead of opaque ints (0/1). This resolves
// finding #2 from dogfooding (enums exposed as raw ints).
func (k Kind) MarshalJSON() ([]byte, error) {
	return json.Marshal(k.String())
}

// UnmarshalJSON accepts EITHER a string ("worker"/"orchestrator") or an int
// (0/1) on the wire. The CLI passes strings; a raw JSON-RPC caller may pass
// either. This resolves finding #8: 'kind' as a string no longer errors with
// 'cannot unmarshal string into Go struct field ... of type session.Kind'.
func (k *Kind) UnmarshalJSON(data []byte) error {
	// Try string first.
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		switch strings.ToLower(s) {
		case "", "worker":
			*k = KindWorker
		case "orchestrator", "orch":
			*k = KindOrchestrator
		default:
			return fmt.Errorf("invalid Kind %q (want worker|orchestrator)", s)
		}
		return nil
	}
	// Fall back to the raw int (the iota value).
	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("Kind must be a string (worker|orchestrator) or int: %w", err)
	}
	switch Kind(n) {
	case KindWorker, KindOrchestrator:
		*k = Kind(n)
		return nil
	default:
		return fmt.Errorf("invalid Kind int %d", n)
	}
}

// Instance is a running instance of claude code.
type Instance struct {
	// ID is the stable, immutable handle of the instance. Allocated at
	// creation, never mutated, never reused. It is the universal handle of
	// the control API (syscalls speak in ID, never Title). Persisted in
	// InstanceData so it survives save/load round-trips.
	ID string
	// Title is the title of the instance.
	Title string
	// Path is the path to the workspace.
	Path string
	// Branch is the branch of the instance.
	Branch string
	// Status is the status of the instance.
	Status Status
	// Program is the program to run in the instance.
	Program string
	// Height is the height of the instance.
	Height int
	// Width is the width of the instance.
	Width int
	// CreatedAt is the time the instance was created.
	CreatedAt time.Time
	// UpdatedAt is the time the instance was last updated.
	UpdatedAt time.Time
	// AutoYes is true if the instance should automatically press enter when prompted.
	AutoYes bool
	// Prompt is the initial prompt to pass to the instance on startup
	Prompt string
	// Kind classifies the instance's role (Worker vs Orchestrator). Defaults
	// to KindWorker for back-compat. Decides which Worktree implementation is
	// bound at Start time; never branched on elsewhere.
	kind Kind

	// host is the execution environment for this instance: how commands run,
	// how the filesystem is touched, how PTYs are allocated, and where the
	// worktree lives. Defaults to LocalHost; v2 sets an SSHHost for remote
	// instances. Read via Host().
	host host.Host

	// DiffStats stores the current git diff statistics
	diffStats *git.DiffStats

	// selectedBranch is the existing branch to start on (empty = new branch from HEAD)
	selectedBranch string

	// The below fields are initialized upon calling Start().

	started bool
	// tmuxSession is the tmux session for the instance.
	tmuxSession *tmux.TmuxSession
	// gitWorktree is the worktree for the instance. Polymorphic: a Worker gets
	// a real *git.GitWorktree, an Orchestrator gets a headlessWorktree. The
	// Kind decides which at Start time (the single factory point); no other
	// code branches on Kind. See Worktree in worktree.go.
	gitWorktree Worktree
}

// ToInstanceData converts an Instance to its serializable form
func (i *Instance) ToInstanceData() InstanceData {
	data := InstanceData{
		ID:        i.ID,
		Title:     i.Title,
		Path:      i.Path,
		Branch:    i.Branch,
		Status:    i.Status,
		Kind:      i.kind,
		Height:    i.Height,
		Width:     i.Width,
		CreatedAt: i.CreatedAt,
		UpdatedAt: time.Now(),
		Program:   i.Program,
		Host:      i.host.Name(),
		AutoYes:   i.AutoYes,
	}

	// Only include worktree data if gitWorktree is initialized
	if i.gitWorktree != nil {
		data.Worktree = GitWorktreeData{
			RepoPath:         i.gitWorktree.GetRepoPath(),
			WorktreePath:     i.gitWorktree.GetWorktreePath(),
			SessionName:      i.SessionLabel(),
			BranchName:       i.gitWorktree.GetBranchName(),
			BaseCommitSHA:    i.gitWorktree.GetBaseCommitSHA(),
			IsExistingBranch: i.gitWorktree.IsExistingBranch(),
		}
	}

	// Only include diff stats if they exist
	if i.diffStats != nil {
		data.DiffStats = DiffStatsData{
			Added:   i.diffStats.Added,
			Removed: i.diffStats.Removed,
			Content: i.diffStats.Content,
		}
	}

	return data
}

// FromInstanceData creates a new Instance from serialized data
func FromInstanceData(data InstanceData) (*Instance, error) {
	// Backfill an ID for instances persisted before this field existed.
	// The ID is immutable from here on; a backfilled one is just as stable
	// as one allocated at creation.
	if data.ID == "" {
		id, err := newInstanceID()
		if err != nil {
			return nil, fmt.Errorf("failed to allocate instance id: %w", err)
		}
		data.ID = id
	}
	h := host.Lookup(data.Host)
	instance := &Instance{
		ID:        data.ID,
		Title:     data.Title,
		Path:      data.Path,
		Branch:    data.Branch,
		Status:    data.Status,
		kind:      data.Kind,
		Height:    data.Height,
		Width:     data.Width,
		CreatedAt: data.CreatedAt,
		UpdatedAt: data.UpdatedAt,
		Program:   data.Program,
		AutoYes:   data.AutoYes,
		host:      h,
	}
	wt, err := restoreWorktree(data.Kind, data.ID, data.Worktree, h)
	if err != nil {
		return nil, err
	}
	instance.gitWorktree = wt
	instance.diffStats = &git.DiffStats{
		Added:   data.DiffStats.Added,
		Removed: data.DiffStats.Removed,
		Content: data.DiffStats.Content,
	}

	if instance.Paused() {
		instance.started = true
		instance.tmuxSession = tmux.NewTmuxSession(instance.SessionName(), instance.Program)
	} else {
		if err := instance.Start(false); err != nil {
			return nil, err
		}
	}

	return instance, nil
}

// Options for creating a new instance
type InstanceOptions struct {
	// Title is the title of the instance.
	Title string
	// Path is the path to the workspace.
	Path string
	// Program is the program to run in the instance (e.g. "claude", "aider --model ollama_chat/gemma3:1b")
	Program string
	// If AutoYes is true, then
	AutoYes bool
	// Branch is an existing branch name to start the session on (empty = new branch from HEAD)
	Branch string
	// ID optionally presets the instance ID. If empty, a new UUID v4 is
	// allocated. Used by tests and backfill paths; production callers leave
	// it empty.
	ID string
	// Kind classifies the instance. Defaults to KindWorker when zero.
	// KindOrchestrator gets a headless worktree at Start time.
	Kind Kind
}

func NewInstance(opts InstanceOptions) (*Instance, error) {
	t := time.Now()

	// The repo path is NOT absolutized here: the host is not known yet (it
	// defaults to Local and is swapped to SSH via SetHost after name entry).
	// Resolving locally now with filepath.Abs would corrupt a remote relative
	// path (it would resolve against the local cwd, pointing at the wrong
	// machine). Path resolution is transport-specific, so it is deferred to
	// Host.ResolveRepoPath at Start time (see buildWorktree), where the host
	// is known. For LocalHost that is filepath.Abs; for SSHHost it is a
	// passthrough so the remote shell resolves ~ and relative paths.
	path := opts.Path

	id := opts.ID
	if id == "" {
		var err error
		id, err = newInstanceID()
		if err != nil {
			return nil, fmt.Errorf("failed to allocate instance id: %w", err)
		}
	}

	return &Instance{
		ID:             id,
		Title:          opts.Title,
		Status:         Ready,
		Path:           path,
		Program:        opts.Program,
		Height:         0,
		Width:          0,
		CreatedAt:      t,
		UpdatedAt:      t,
		AutoYes:        false,
		kind:           opts.Kind,
		host:           host.Local,
		selectedBranch: opts.Branch,
	}, nil
}

// newInstanceID allocates a random RFC 4122 v4 UUID string. Uses crypto/rand
// so IDs are unguessable (important: they are the handle by which an
// orchestrator's control API addresses instances). No external dependency.
func newInstanceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	// Set version (4) and variant (10xx) bits per RFC 4122.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// GetID returns the immutable instance ID. It is the canonical handle used by
// the control API.
func (i *Instance) GetID() string {
	return i.ID
}

// SessionName returns the tmux session name for this instance. It is the
// title sanitized per tmux's rules, with a short hash of the immutable
// instance ID appended so that two instances cannot collide on the same
// tmux session name even when they share a title (the
// collision-from-shared-titles regression: a live session under a shared
// name masked orphaned worktrees during liveness reconciliation). The ID
// hash is stable across daemon restarts, so a persisted instance resumes
// the same session name. The host alias is never included (PII: decision 5).
func (i *Instance) SessionName() string {
	return tmux.SessionName(i.SessionLabel())
}

// SessionLabel is the pre-sanitization, pre-prefix label that uniquely
// identifies this instance's tmux session: "<title>-<idhash8>". It is the
// input to tmux.SessionName. Exposed so storage and tests can predict the
// session name without re-deriving the hash.
func (i *Instance) SessionLabel() string {
	sum := sha256.Sum256([]byte(i.ID))
	return i.Title + "-" + hex.EncodeToString(sum[:])[:8]
}

func (i *Instance) Kind() Kind {
	return i.kind
}

func (i *Instance) RepoName() (string, error) {
	if !i.started {
		return "", fmt.Errorf("cannot get repo name for instance that has not been started")
	}
	if i.gitWorktree == nil {
		// An orchestrator (headless worktree) or a mid-construction instance
		// has no repo name. Guard the deref so callers can summarize safely.
		return "", nil
	}
	return i.gitWorktree.GetRepoName(), nil
}

func (i *Instance) SetStatus(status Status) {
	i.Status = status
}

// SetSelectedBranch sets the branch to use when starting the instance.
func (i *Instance) SetSelectedBranch(branch string) {
	i.selectedBranch = branch
}

// SelectedBranch returns the existing branch the instance will start on, or
// empty when a new branch from HEAD is to be created. It is the read twin of
// SetSelectedBranch, used by the TUI to build kernel spawn options from a
// draft instance (C3.3).
func (i *Instance) SelectedBranch() string {
	return i.selectedBranch
}

// SetAutoYes toggles the per-instance AutoYes flag. Persisted on InstanceData
// and respected by the daemon (which no longer forces it globally). Used by
// the TUI toggle: flipping on a remote host is allowed but the user should
// be aware it auto-approves agent actions on a distant machine.
func (i *Instance) SetAutoYes(on bool) {
	i.AutoYes = on
}

// pausedCommitMessage builds the local commit message used when pausing an
// instance. It is a function of the instance Title and the pause time only —
// never of the host. The signature enforces this PII invariant (decision 5):
// no host can be threaded in, so a remote host's alias can never leak into git
// history via the pause commit. Tested by TestInstance_PII_HostAliasNotInArtifacts.
func pausedCommitMessage(title string, t time.Time) string {
	return fmt.Sprintf("[claudesquad] update from '%s' on %s (paused)", title, t.Format(time.RFC822))
}

// buildWorktree is the SINGLE factory point that branches on Kind to pick
// the Worktree implementation for a newly-created instance. A Worker gets a
// real *git.GitWorktree; an Orchestrator gets a headlessWorktree. No other
// code in the package branches on Kind — the difference is bound here and
// hidden behind the Worktree interface everywhere else.
func (i *Instance) buildWorktree() (Worktree, string, error) {
	if i.kind == KindOrchestrator {
		wt, err := newHeadlessWorktree(i.ID)
		if err != nil {
			return nil, "", fmt.Errorf("failed to build headless worktree: %w", err)
		}
		// No branch for an orchestrator.
		return wt, "", nil
	}

	// Worker: real git worktree in the host's worktree dir.
	worktreeDir, err := i.host.WorktreeDir()
	if err != nil {
		return nil, "", fmt.Errorf("failed to resolve worktree directory: %w", err)
	}
	// Resolve the repo path for THIS host's transport. For LocalHost that's
	// filepath.Abs (so a stored path survives a cwd change); for SSHHost it's
	// a passthrough so the remote shell resolves ~ and relative paths against
	// the remote $HOME. Done here, after the host is known, rather than in
	// NewInstance (where the host isn't set yet).
	repoPath := i.host.ResolveRepoPath(i.Path)
	if i.selectedBranch != "" {
		gitWorktree, err := git.NewGitWorktreeFromBranchWithDeps(repoPath, i.selectedBranch, i.SessionLabel(), i.host.Executor(), i.host.FS(), worktreeDir)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create git worktree from branch: %w", err)
		}
		return gitWorktree, i.selectedBranch, nil
	}
	gitWorktree, branchName, err := git.NewGitWorktreeWithDeps(repoPath, i.SessionLabel(), i.host.Executor(), i.host.FS(), worktreeDir)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create git worktree: %w", err)
	}
	return gitWorktree, branchName, nil
}

// restoreWorktree is the factory point for an instance rebuilt from storage:
// it picks the Worktree implementation from the persisted Kind + worktree
// data. Paired with buildWorktree — together they are the only Kind branches.
func restoreWorktree(kind Kind, id string, data GitWorktreeData, h host.Host) (Worktree, error) {
	if kind == KindOrchestrator {
		wt, err := newHeadlessWorktree(id)
		if err != nil {
			return nil, fmt.Errorf("failed to build headless worktree: %w", err)
		}
		return wt, nil
	}
	worktreeDir, err := h.WorktreeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to resolve worktree directory: %w", err)
	}
	return git.NewGitWorktreeFromStorageWithDeps(
		data.RepoPath, data.WorktreePath, data.SessionName, data.BranchName,
		data.BaseCommitSHA, data.IsExistingBranch,
		h.Executor(), h.FS(), worktreeDir,
	), nil
}

// firstTimeSetup is true if this is a new instance. Otherwise, it's one loaded from storage.
func (i *Instance) Start(firstTimeSetup bool) error {
	if i.Title == "" {
		return fmt.Errorf("instance title cannot be empty")
	}

	var tmuxSession *tmux.TmuxSession
	if i.tmuxSession != nil {
		// Use existing tmux session (useful for testing)
		tmuxSession = i.tmuxSession
	} else {
	// Create new tmux session bound to this instance's host (local today;
		// v2 SSHHost swaps in ssh-backed deps here). The session name is derived
		// from i.Title — never the host alias — so a remote host never
		// appears in tmux session names (PII discipline, decision 5).
		//
		// A short hash of the instance ID is appended so that two instances
		// sharing a title (duplicate titles, or an orchestrator+worker with
		// the same name) cannot collide on the same tmux session name. Without
		// this, the shared session masked orphaned worktrees during liveness
		// reconciliation (the collision-from-shared-titles regression).
		tmuxSession = tmux.NewTmuxSessionWithDeps(i.SessionName(), i.Program, i.host.PtyFactory(), i.host.Executor())
	}
	i.tmuxSession = tmuxSession

	if firstTimeSetup {
		wt, branch, err := i.buildWorktree()
		if err != nil {
			return err
		}
		i.gitWorktree = wt
		i.Branch = branch
	}

	// Setup error handler to cleanup resources on any error
	var setupErr error
	defer func() {
		if setupErr != nil {
			if cleanupErr := i.Kill(); cleanupErr != nil {
				setupErr = fmt.Errorf("%v (cleanup error: %v)", setupErr, cleanupErr)
			}
		} else {
			i.started = true
		}
	}()

	if !firstTimeSetup {
		// Reuse existing session
		if err := tmuxSession.Restore(); err != nil {
			setupErr = fmt.Errorf("failed to restore existing session: %w", err)
			return setupErr
		}
	} else {
		// Setup git worktree first
		if err := i.gitWorktree.Setup(); err != nil {
			setupErr = fmt.Errorf("failed to setup git worktree: %w", err)
			return setupErr
		}

		// Create new session
		if err := i.tmuxSession.Start(i.gitWorktree.GetWorktreePath()); err != nil {
			// Cleanup git worktree if tmux session creation fails
			if cleanupErr := i.gitWorktree.Cleanup(); cleanupErr != nil {
				err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
			}
			setupErr = fmt.Errorf("failed to start new session: %w", err)
			return setupErr
		}
	}

	i.SetStatus(Running)

	return nil
}

// Kill terminates the instance and cleans up all resources
func (i *Instance) Kill() error {
	if !i.started {
		// If instance was never started, just return success
		return nil
	}

	var errs []error

	// Always try to cleanup both resources, even if one fails
	// Clean up tmux session first since it's using the git worktree
	if i.tmuxSession != nil {
		if err := i.tmuxSession.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close tmux session: %w", err))
		}
	}

	// Then clean up git worktree
	if i.gitWorktree != nil {
		if err := i.gitWorktree.Cleanup(); err != nil {
			errs = append(errs, fmt.Errorf("failed to cleanup git worktree: %w", err))
		}
	}

	return i.combineErrors(errs)
}

// combineErrors combines multiple errors into a single error
func (i *Instance) combineErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}

	errMsg := "multiple cleanup errors occurred:"
	for _, err := range errs {
		errMsg += "\n  - " + err.Error()
	}
	return fmt.Errorf("%s", errMsg)
}

func (i *Instance) Preview() (string, error) {
	if !i.started || i.Status == Paused {
		return "", nil
	}
	return i.tmuxSession.CapturePaneContent()
}

func (i *Instance) HasUpdated() (updated bool, status program.Status) {
	if !i.started {
		return false, program.StatusUnknown
	}
	return i.tmuxSession.HasUpdated()
}

// CheckAndHandleTrustPrompt checks for and dismisses a prompt the agent's
// adapter knows how to resolve (e.g. a trust or MCP approval prompt), for
// any program. Unknown programs resolve to NoOpAdapter which detects nothing,
// so this is safe to call regardless of the program. Agent-specific knowledge
// lives in program.Adapter, not here.
func (i *Instance) CheckAndHandleTrustPrompt() bool {
	if !i.started || i.tmuxSession == nil {
		return false
	}
	return i.tmuxSession.CheckAndHandleTrustPrompt()
}

// TapEnter sends an enter key press to the tmux session if AutoYes is enabled.
func (i *Instance) TapEnter() {
	if !i.started || !i.AutoYes {
		return
	}
	if err := i.tmuxSession.TapEnter(); err != nil {
		log.ErrorLog.Printf("error tapping enter: %v", err)
	}
}

func (i *Instance) Attach() (chan struct{}, error) {
	if !i.started {
		return nil, fmt.Errorf("cannot attach instance that has not been started")
	}
	return i.tmuxSession.Attach()
}

func (i *Instance) SetPreviewSize(width, height int) error {
	if !i.started || i.Status == Paused {
		return fmt.Errorf("cannot set preview size for instance that has not been started or " +
			"is paused")
	}
	return i.tmuxSession.SetDetachedSize(width, height)
}

// GetGitWorktree returns the worktree for the instance. The concrete type
// depends on the instance Kind: a *git.GitWorktree for a Worker, a
// headlessWorktree for an Orchestrator. Callers operate on the Worktree
// interface, so they are agnostic to the Kind.
func (i *Instance) GetGitWorktree() (Worktree, error) {
	if !i.started {
		return nil, fmt.Errorf("cannot get git worktree for instance that has not been started")
	}
	return i.gitWorktree, nil
}

// GetWorktreePath returns the worktree path for the instance, or empty string if unavailable
func (i *Instance) GetWorktreePath() string {
	if i.gitWorktree == nil {
		return ""
	}
	return i.gitWorktree.GetWorktreePath()
}

func (i *Instance) Started() bool {
	return i.started
}

// SetTitle sets the title of the instance. Returns an error if the instance has started.
// We cant change the title once it's been used for a tmux session etc.
func (i *Instance) SetTitle(title string) error {
	if i.started {
		return fmt.Errorf("cannot change title of a started instance")
	}
	i.Title = title
	return nil
}

func (i *Instance) Paused() bool {
	return i.Status == Paused
}

// TmuxAlive returns true if the tmux session is alive. This is a sanity check before attaching.
func (i *Instance) TmuxAlive() bool {
	if i.tmuxSession == nil {
		return false
	}
	return i.tmuxSession.DoesSessionExist()
}

// IsWorktreeOrphaned reports whether the instance's git worktree is missing
// from the filesystem. It is the filesystem-side liveness check that pairs
// with TmuxAlive: a Worker instance whose worktree directory or its .git
// entry is gone is orphaned, even if a tmux session under its name still
// exists (e.g. another instance shares the same sanitized session name —
// the collision-from-shared-titles regression). ReconcileLiveness demotes an
// instance to Dead when EITHER signal is gone, so a collided-name session
// does not mask an orphaned worktree.
//
// An Orchestrator (headless worktree) is never orphaned — its control dir
// is recreatable and the orchestrator supervises, it does not edit a repo.
// A not-yet-started instance, or one with no worktree handle, is treated as
// not orphaned (not a reconciliation candidate).
func (i *Instance) IsWorktreeOrphaned() bool {
	if i.gitWorktree == nil {
		return false
	}
	valid, err := i.gitWorktree.IsValidWorktree()
	if err != nil {
		// Could not probe the FS — do NOT claim orphaned. A probe failure is
		// the same class as a tmux timeout: never demote on uncertainty.
		return false
	}
	return !valid
}

// Pause stops the tmux session and removes the worktree, preserving the branch
func (i *Instance) Pause() error {
	if !i.started {
		return fmt.Errorf("cannot pause instance that has not been started")
	}
	if i.Status == Paused {
		return fmt.Errorf("instance is already paused")
	}

	var errs []error

	// If the worktree is orphaned (path or .git missing), git cannot operate
	// on it. Skip dirty check and Remove, prune any lingering metadata, then
	// transition to Paused so the user can recover via Resume.
	if valid, err := i.gitWorktree.IsValidWorktree(); err != nil {
		errs = append(errs, fmt.Errorf("failed to validate worktree: %w", err))
		log.ErrorLog.Print(err)
	} else if !valid {
		log.WarningLog.Printf("worktree at %s is orphaned; skipping dirty check and remove",
			i.gitWorktree.GetWorktreePath())
		if err := i.tmuxSession.DetachSafely(); err != nil {
			errs = append(errs, fmt.Errorf("failed to detach tmux session: %w", err))
			log.ErrorLog.Print(err)
		}
		// Drop any leftover directory so a future Resume's `git worktree add` won't conflict.
		if err := i.gitWorktree.RemoveWorktreeDir(); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove orphaned worktree directory: %w", err))
			log.ErrorLog.Print(err)
		}
		if err := i.gitWorktree.Prune(); err != nil {
			errs = append(errs, fmt.Errorf("failed to prune git worktrees: %w", err))
			log.ErrorLog.Print(err)
		}
		i.SetStatus(Paused)
		_ = clipboard.WriteAll(i.gitWorktree.GetBranchName())
		return i.combineErrors(errs)
	}

	// Check if there are any changes to commit
	if dirty, err := i.gitWorktree.IsDirty(); err != nil {
		errs = append(errs, fmt.Errorf("failed to check if worktree is dirty: %w", err))
		log.ErrorLog.Print(err)
	} else if dirty {
		// Commit changes locally (without pushing to GitHub). The message is a
		// function of the instance Title + time only — never the host (PII
		// discipline, PLAN-ssh-v2.md decision 5): a remote host's alias must
		// not leak into git history.
		commitMsg := pausedCommitMessage(i.Title, time.Now())
		if err := i.gitWorktree.CommitChanges(commitMsg); err != nil {
			errs = append(errs, fmt.Errorf("failed to commit changes: %w", err))
			log.ErrorLog.Print(err)
			// Return early if we can't commit changes to avoid corrupted state
			return i.combineErrors(errs)
		}
	}

	// Detach from tmux session instead of closing to preserve session output
	if err := i.tmuxSession.DetachSafely(); err != nil {
		errs = append(errs, fmt.Errorf("failed to detach tmux session: %w", err))
		log.ErrorLog.Print(err)
		// Continue with pause process even if detach fails
	}

	// Check if worktree exists before trying to remove it
	if i.gitWorktree.WorktreeDirExists() {
		// Remove worktree but keep branch
		if err := i.gitWorktree.Remove(); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove git worktree: %w", err))
			log.ErrorLog.Print(err)
			return i.combineErrors(errs)
		}

		// Only prune if remove was successful
		if err := i.gitWorktree.Prune(); err != nil {
			errs = append(errs, fmt.Errorf("failed to prune git worktrees: %w", err))
			log.ErrorLog.Print(err)
			return i.combineErrors(errs)
		}
	}

	i.SetStatus(Paused)
	_ = clipboard.WriteAll(i.gitWorktree.GetBranchName())

	if err := i.combineErrors(errs); err != nil {
		log.ErrorLog.Print(err)
		return err
	}
	return nil
}

// Resume recreates the worktree and restarts the tmux session
func (i *Instance) Resume() error {
	if !i.started {
		return fmt.Errorf("cannot resume instance that has not been started")
	}
	if i.Status != Paused {
		return fmt.Errorf("can only resume paused instances")
	}

	// Check if branch is checked out
	if checked, err := i.gitWorktree.IsBranchCheckedOut(); err != nil {
		log.ErrorLog.Print(err)
		return fmt.Errorf("failed to check if branch is checked out: %w", err)
	} else if checked {
		return fmt.Errorf("cannot resume: branch is checked out, please switch to a different branch")
	}

	// Setup git worktree
	if err := i.gitWorktree.Setup(); err != nil {
		log.ErrorLog.Print(err)
		return fmt.Errorf("failed to setup git worktree: %w", err)
	}

	// Check if tmux session still exists from pause, otherwise create new one
	if i.tmuxSession.DoesSessionExist() {
		// Session exists, just restore PTY connection to it
		if err := i.tmuxSession.Restore(); err != nil {
			log.ErrorLog.Print(err)
			// If restore fails, fall back to creating new session
			if err := i.tmuxSession.Start(i.gitWorktree.GetWorktreePath()); err != nil {
				log.ErrorLog.Print(err)
				// Cleanup git worktree if tmux session creation fails
				if cleanupErr := i.gitWorktree.Cleanup(); cleanupErr != nil {
					err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
					log.ErrorLog.Print(err)
				}
				return fmt.Errorf("failed to start new session: %w", err)
			}
		}
	} else {
		// Create new tmux session
		if err := i.tmuxSession.Start(i.gitWorktree.GetWorktreePath()); err != nil {
			log.ErrorLog.Print(err)
			// Cleanup git worktree if tmux session creation fails
			if cleanupErr := i.gitWorktree.Cleanup(); cleanupErr != nil {
				err = fmt.Errorf("%v (cleanup error: %v)", err, cleanupErr)
				log.ErrorLog.Print(err)
			}
			return fmt.Errorf("failed to start new session: %w", err)
		}
	}

	i.SetStatus(Running)
	return nil
}

// UpdateDiffStats updates the git diff statistics for this instance
func (i *Instance) UpdateDiffStats() error {
	if !i.started {
		i.diffStats = nil
		return nil
	}

	if i.Status == Paused {
		// Keep the previous diff stats if the instance is paused
		return nil
	}

	stats := i.gitWorktree.Diff()
	if stats == nil {
		// A headless worktree (orchestrator) has no git repo and returns a nil
		// Diff. Nothing to report; leave diffStats clear and avoid nil-deref'ing
		// stats.Error below. This is what keeps the daemon's poll loop alive when
		// an orchestrator is in the fleet.
		i.diffStats = nil
		return nil
	}
	if stats.Error != nil {
		if strings.Contains(stats.Error.Error(), "base commit SHA not set") {
			// Worktree is not fully set up yet, not an error
			i.diffStats = nil
			return nil
		}
		return fmt.Errorf("failed to get diff stats: %w", stats.Error)
	}

	i.diffStats = stats
	return nil
}

// ComputeDiff runs the expensive git diff I/O and returns the result without
// mutating instance state. Safe to call from a background goroutine.
func (i *Instance) ComputeDiff() *git.DiffStats {
	if !i.started || i.Status == Paused {
		return nil
	}
	return i.gitWorktree.Diff()
}

// ComputeDiffNumstat runs a lightweight git diff --numstat and returns only the
// added/removed line counts (Content is left empty). Safe to call from a
// background goroutine. Use this for instances whose full diff content is not
// currently needed so we avoid keeping large diffs in memory.
func (i *Instance) ComputeDiffNumstat() *git.DiffStats {
	if !i.started || i.Status == Paused {
		return nil
	}
	return i.gitWorktree.DiffNumstat()
}

// SetDiffStats sets the diff statistics on the instance. Should be called from
// the main event loop to avoid data races with View.
func (i *Instance) SetDiffStats(stats *git.DiffStats) {
	i.diffStats = stats
}

// GetDiffStats returns the current git diff statistics
func (i *Instance) GetDiffStats() *git.DiffStats {
	return i.diffStats
}

// SendPrompt sends a prompt to the tmux session
func (i *Instance) SendPrompt(prompt string) error {
	if !i.started {
		return fmt.Errorf("instance not started")
	}
	if i.tmuxSession == nil {
		return fmt.Errorf("tmux session not initialized")
	}
	if err := i.tmuxSession.SendKeys(prompt); err != nil {
		return fmt.Errorf("error sending keys to tmux session: %w", err)
	}

	// Brief pause to prevent carriage return from being interpreted as newline
	time.Sleep(100 * time.Millisecond)
	if err := i.tmuxSession.TapEnter(); err != nil {
		return fmt.Errorf("error tapping enter: %w", err)
	}

	return nil
}

// WaitForPaneStable polls the tmux pane content until it stops changing, then
// returns. Spawn uses it to defer the initial prompt until the agent CLI has
// finished its boot animation: keystrokes — and especially the carriage
// return that submits the prompt — sent while the CLI is still rendering its
// welcome banner / initializing MCP / drawing the input box are routinely
// swallowed or consumed by the boot sequence, so the prompt text lands in the
// input box but never gets submitted, as if Enter had never been pressed.
// Waiting for the pane to settle guarantees the input handler is live before
// we type.
//
// Stability is defined as `stableSamples` consecutive identical captures
// spaced `interval` apart. This is deliberately agent-agnostic: it needs no
// per-agent "ready" marker (the adapters do not reliably emit one at boot —
// Pi's cs2:ready sentinel only appears after a completed turn, and Claude's
// ready marker only appears in refusal dialogs). A timeout is NOT an error:
// if the pane never settles (e.g. an animated spinner that updates every
// tick), we fall back to the previous best-effort behaviour and let the caller
// send the prompt anyway. Capture errors during very early boot are treated
// as "not stable yet" rather than aborting.
func (i *Instance) WaitForPaneStable(timeout time.Duration) error {
	if !i.started || i.tmuxSession == nil {
		return nil
	}
	const (
		interval      = 150 * time.Millisecond
		stableSamples = 3
	)
	deadline := time.Now().Add(timeout)
	prev := ""
	stable := 0
	for {
		content, err := i.tmuxSession.CapturePaneContent()
		switch {
		case err != nil:
			// Transient capture failure (pane not ready yet) — keep waiting.
			stable = 0
		case content == prev:
			stable++
		default:
			prev = content
			stable = 1
		}
		if stable >= stableSamples {
			return nil
		}
		if !deadline.After(time.Now()) {
			return nil
		}
		time.Sleep(interval)
	}
}

// PreviewFullHistory captures the entire tmux pane output including full scrollback history
func (i *Instance) PreviewFullHistory() (string, error) {
	if !i.started || i.Status == Paused {
		return "", nil
	}
	if i.tmuxSession == nil {
		// No tmux session bound (e.g. an orchestrator, or a test instance).
		return "", nil
	}
	return i.tmuxSession.CapturePaneContentWithOptions("-", "-")
}

// Host returns the execution environment for this instance (local today; v2
// SSHHost for remote instances). Read-only access for callers that need the
// host's name or AutoYes policy.
func (i *Instance) Host() host.Host {
	return i.host
}

// SetHost sets the execution environment. Used by the creation flow after the
// user picks a host (before Start). Must not be called after Start: the
// tmux/git deps are bound at Start time from the host, so changing it later
// would leave stale sessions pointing at the wrong host.
func (i *Instance) SetHost(h host.Host) error {
	if i.started {
		return fmt.Errorf("cannot change host of a started instance")
	}
	i.host = h
	return nil
}

// SetTmuxSession sets the tmux session for testing purposes
func (i *Instance) SetTmuxSession(session *tmux.TmuxSession) {
	i.tmuxSession = session
}

// SendKeys sends keys to the tmux session
func (i *Instance) SendKeys(keys string) error {
	if !i.started || i.Status == Paused {
		return fmt.Errorf("cannot send keys to instance that has not been started or is paused")
	}
	return i.tmuxSession.SendKeys(keys)
}

// MarkStartedForTest sets the started flag without running tmux. It is a test
// seam for packages (e.g. kernel) that need an instance to look started for
// in-memory unit tests without a real PTY. Not for production use.
func (i *Instance) MarkStartedForTest() {
	i.started = true
}
