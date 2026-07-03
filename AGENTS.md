# AGENTS.md — cs2

> Guide for any agent (human or AI) working on this codebase. Read this first.

## What cs2 is

**cs2 is an orchestrator for agentic work sessions.** It is a terminal UI that
manages multiple AI coding agents (Claude Code, Pi, Aider, Codex, Gemini, …)
running concurrently, each in its own isolated git worktree, so they can work
in parallel without stepping on each other.

cs2 is a fork of [claude-squad](https://github.com/smtg-ai/claude-squad). The
fork exists to (a) make agent support modular and (b) add multi-repo
orchestration. It ships as a separate `cs2` binary; the official Homebrew `cs`
is left untouched for side-by-side comparison.

## Goals

1. **Modular agent support.** Adding a new agent (Pi, Codex, Amp, …) is one
   file under `program/` + one `Register` line. It never touches the tmux core,
   the TUI, or the daemon. See `program/adapter.go` for the seam.
2. **Multi-repo orchestration.** The TUI centralizes instances running across
   several different repositories in one place (a future capability; the
   upstream was hardcoded to a single repo = the cwd).

## Non-negotiable rules

### Every instance is bound to a repo worktree

- Each instance runs inside a **git worktree** of a real git repository.
- An instance **cannot exist without a linked repo** — there is no "free"
  instance floating outside a worktree.
- An instance **cannot modify `main`** (or the checked-out branch of the host
  repo) unless the user explicitly asks for it. By default every instance works
  on its own isolated branch in its own worktree.
- This isolation is the whole point: parallel agents must not corrupt each
  other's working state.

### The TUI is the single source of truth for running sessions

- All running instances, their status, and their diffs are visible in one
  central TUI.
- The user supervises, attaches, pauses, checks out, and pushes from there.

## Philosophy

- **Clean, modular code following best practices.** Particular attention to
  **DRY** (no duplicated knowledge — e.g. agent-specific strings live in one
  adapter, not scattered) and **SRP** (each package/function does one thing:
  `program/` knows about agents, `session/tmux/` knows about tmux, `session/git/`
  knows about worktrees, `ui/` knows about rendering).
- **Deep modules over shallow ones.** Small interfaces hiding large behaviour.
  The `program.Adapter` seam is the canonical example: 3 methods, pure
  `Detect(content) (Status, *Prompt)`, fully testable without tmux or a PTY.
- **One adapter means a hypothetical seam; two means a real one.** Don't add
  abstractions speculatively.
- **Design / UX comes last.** Architecture and mechanics are prioritized over
  visual polish. Do not let TUI redesign block structural work.
- **Standalone and agent-agnostic.** cs2 must not be coupled to any one agent
  (not even Pi). Pi is one agent among equals. Never port the supervisor into a
  Pi extension — that would break supervising Claude/Codex/etc.
- **No sensitive leaks.** Use neutral placeholders (`<provider> <model>`) in
  tests and docs, never real account/provider names.

## Local conventions

- `cs2` binary is built to `~/bin/cs2` (on `PATH` via `~/.zshrc`). Rebuild after
  Go changes: `cd ~/cs-multirepo && go build -o ~/bin/cs2 .`
- Go 1.26+ (`brew install go`). Baseline: `go build ./... && go test ./...` green.
- cs2 uses a dedicated `~/.cs2/` config dir (separate from the official `cs`'
  `~/.claude-squad/`). Cold start: no migration from `cs`. See `PLAN-multi-repo.md`.
- See `PLAN.md` for the (completed) modularity plan. See `PLAN-multi-repo.md`
  for the multi-repo plan.
