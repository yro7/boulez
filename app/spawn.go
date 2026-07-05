package app

import (
	"claude-squad/host"
	"claude-squad/session"
	"claude-squad/session/git"
	"claude-squad/session/tmux"
	"fmt"
	"time"
)

// SpawnOptions are the inputs to Spawn: everything needed to create and start
// a new instance programmatically, with no TUI. This is the programmatic twin
// of the TUI's startNewInstance flow, and the foundation of the control API
// (an orchestrator's `spawn_worker` syscall calls Spawn).
type SpawnOptions struct {
	// Repo is the path to the git repository the new instance works in.
	// Required.
	Repo string
	// Branch is the branch to start on. Empty = a new branch from HEAD
	// (the cs2/<title> convention). When set, the branch is created from HEAD
	// if absent (unless BranchMustExist is set, which requires it to pre-exist).
	Branch string
	// Prompt is the initial task to send to the agent once it has started.
	// Empty = no initial prompt (the instance starts in Ready).
	Prompt string
	// Program is the agent command to run (e.g. "claude", "aider ...").
	// Empty falls back to DefaultProgram.
	Program string
	// Title is the instance title (also drives the branch name). If empty,
	// Spawn derives one.
	Title string
	// Host is the execution environment. Nil = LocalHost.
	Host host.Host
	// Kind classifies the instance (Worker vs Orchestrator). Defaults to
	// KindWorker when zero. An orchestrator's spawn_worker guard refuses to
	// spawn a Worker if the *caller* is itself a Worker — that guard lives in
	// the kernel (Spawn's caller), not here; Spawn itself just builds whatever
	// Kind it is told to.
	Kind session.Kind

	// BranchMustExist controls what happens when Branch is set but does not
	// exist in the repo. The default (false) makes cs2 create the branch from
	// HEAD — the orchestrator-friendly behaviour (deterministic branch names
	// without pre-creating them). When true, Spawn refuses with
	// git.ErrBranchNotFound (mapped to BRANCH_NOT_FOUND on the wire) — this
	// restores the pre-fix behaviour for callers that want to resume work on a
	// branch that must already be there. Ignored when Branch is empty.
	BranchMustExist bool

	// tmuxSession is a test-only seam: when non-nil, Spawn installs it on the
	// instance before Start so Start reuses it instead of creating a real tmux
	// session. This lets Spawn be tested without spawning a real agent process.
	// Production callers leave it nil.
	tmuxSession *tmux.TmuxSession
}

// DefaultProgram is the agent command used when SpawnOptions.Program is empty.
// It mirrors the TUI's default (cfg.GetProgram()). Spawn callers that want
// the global default should pass it explicitly; this constant keeps the
// zero-value of SpawnOptions meaningful for tests.
const DefaultProgram = "claude"

// Spawn creates and starts a new instance non-interactively: NewInstance →
// (optional) SetHost → Start(true) → (optional) SendPrompt. It returns the
// started instance (with its ID allocated) and commits no state to storage —
// persistence is the caller's responsibility (the kernel owns storage).
//
// This is the programmatic entry point an orchestrator's `spawn_worker`
// syscall calls. It is deliberately TUI-free and tmux-coupled only through
// Instance.Start (which the kernel will own the lifecycle of).
func Spawn(opts SpawnOptions) (*session.Instance, error) {
	// An orchestrator supervises the fleet — it has no repo/worktree, so it
	// does not require a Repo. Workers must have one.
	if opts.Repo == "" && opts.Kind != session.KindOrchestrator {
		return nil, fmt.Errorf("spawn: repo is required")
	}
	program := opts.Program
	if program == "" {
		program = DefaultProgram
	}
	title := opts.Title
	if title == "" {
		title = deriveSpawnTitle(opts)
	}

	// When a branch is requested, ensure it exists before the instance is
	// created: by default create it from HEAD (deterministic names for an
	// orchestrator); with BranchMustExist, refuse if absent. Doing this here
	// (not inside the worktree setup) keeps the "create if absent" policy in
	// one place and the worktree layer purely mechanical.
	if opts.Branch != "" {
		if err := git.EnsureBranch(opts.Repo, opts.Branch, opts.BranchMustExist); err != nil {
			return nil, fmt.Errorf("spawn: %w", err)
		}
	}

	inst, err := session.NewInstance(session.InstanceOptions{
		Title:   title,
		Path:    opts.Repo,
		Program: program,
		Branch:  opts.Branch,
		Kind:    opts.Kind,
	})
	if err != nil {
		return nil, fmt.Errorf("spawn: create instance: %w", err)
	}

	if opts.Host != nil {
		if err := inst.SetHost(opts.Host); err != nil {
			return nil, fmt.Errorf("spawn: set host: %w", err)
		}
	}

	// Test seam: install a pre-built tmux session so Start reuses it instead
	// of creating one that runs a real agent process. No-op in production.
	if opts.tmuxSession != nil {
		inst.SetTmuxSession(opts.tmuxSession)
	}

	// Start binds the tmux session + worktree. firstTimeSetup=true so the
	// worktree is created (not restored).
	if err := inst.Start(true); err != nil {
		return nil, fmt.Errorf("spawn: start instance: %w", err)
	}

	// New instances inherit AutoYes from their host's policy, mirroring the
	// TUI flow (app.go instanceStartedMsg). Local follows the global flag;
	// remote defaults to off.
	inst.SetAutoYes(inst.Host().AutoYesDefault())

	if opts.Prompt != "" {
		// Wait for the agent CLI to finish booting before sending the prompt.
		// Without this, the prompt text is typed into the input box but the
		// Enter that submits it is sent while the CLI is still rendering (welcome
		// banner, MCP init, …) and gets swallowed — the prompt sits unsent, as if
		// Enter had never been pressed. We wait for the pane to stabilize so the
		// input handler is live before we type + submit. Best-effort: on timeout
		// we proceed anyway, preserving the previous behaviour for agents that
		// never settle.
		_ = inst.WaitForPaneStable(8 * time.Second)
		if err := inst.SendPrompt(opts.Prompt); err != nil {
			// The instance is started; a prompt failure is not fatal to the
			// spawn itself. Surface it but return the running instance.
			return inst, fmt.Errorf("spawn: instance started but initial prompt failed: %w", err)
		}
	}

	return inst, nil
}

// deriveSpawnTitle builds a title when SpawnOptions.Title is empty. The title
// also drives the branch name (cs2/<sanitized title>), so it must be unique
// across concurrent spawns to avoid branch collisions. We suffix with a
// monotonic-enough timestamp; the instance ID (allocated inside NewInstance)
// is the true unique handle, but the title must already be unique at Start
// time because the branch name derives from it.
func deriveSpawnTitle(opts SpawnOptions) string {
	base := "spawn"
	if opts.Kind == session.KindOrchestrator {
		base = "orchestrator"
	}
	return fmt.Sprintf("%s-%d", base, time.Now().UnixNano())
}
