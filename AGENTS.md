# AGENTS.md — Boulez

> Guide for any agent (human or AI) working on this codebase. Read this first.

## What Boulez is

**Boulez is an orchestrator for agentic work sessions.** It is a kernel +
daemon that manages multiple AI coding agents (Claude Code, Pi, Aider, Codex,
Gemini, …) running concurrently, each in its own isolated git worktree, so they
can work in parallel without stepping on each other. A TUI (one of several
possible consumers) supervises the whole fleet.

Boulez is derived from [claude-squad](https://github.com/smtg-ai/claude-squad)
(upstream commit `5a604f7`, v1.0.19). It has since been restructured around a
**kernel + daemon** architecture: the TUI is only a consumer that can be
launched from anywhere, multi-env (SSH) and multi-repo orchestration have been
added, and the project is no longer rebased against upstream (too many
differences). See the [Origin](./README.md#origin) section of the README.

## Goals

1. **Modular agent support.** Adding a new agent (Pi, Codex, Amp, …) is one
   file under `program/` + one `Register` line. It never touches the tmux core,
   the TUI, or the daemon. See `program/adapter.go` for the seam.
2. **Multi-repo orchestration.** The TUI centralizes instances running across
   several different repositories in one place.
3. **Multi-env (SSH).** An instance's whole environment (worktree, tmux, agent)
   can run on a remote machine over SSH while being supervised locally.

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

### The daemon is the single source of truth for running sessions

- The daemon owns the kernel / control authority. All running instances, their
  status, and their diffs are visible through it.
- The TUI and `boulez ctl` are its clients. The user supervises, attaches,
  pauses, checks out, and pushes from the TUI.

## Philosophy

- **Clean, modular code following best practices.** Particular attention to
  **DRY** (no duplicated knowledge — e.g. agent-specific strings live in one
  adapter, not scattered) and **SRP** (each package/function does one thing:
  `program/` knows about agents, `session/tmux/` knows about tmux, `session/git/`
  knows about worktrees, `ui/` knows about rendering, `kernel/` + `daemon/`
  know about the control authority).
- **Deep modules over shallow ones.** Small interfaces hiding large behaviour.
  The `program.Adapter` seam is the canonical example: 3 methods, pure
  `Detect(content) (Status, *Prompt)`, fully testable without tmux or a PTY.
- **One adapter means a hypothetical seam; two means a real one.** Don't add
  abstractions speculatively.
- **Design / UX comes last.** Architecture and mechanics are prioritized over
  visual polish. Do not let TUI redesign block structural work.
- **Standalone and agent-agnostic.** Boulez must not be coupled to any one
  agent (not even Pi). Pi is one agent among equals. Never port the supervisor
  into a Pi extension — that would break supervising Claude/Codex/etc.
- **No sensitive leaks.** Use neutral placeholders (`<provider> <model>`) in
  tests and docs, never real account/provider names.

## Local conventions

- Module path: `github.com/yro7/boulez`. Binary: `boulez`.
- Build: `go build -o boulez .`. Baseline: `go build ./... && go test ./...` green.
- Go 1.26+ (`brew install go`).
- Boulez uses a dedicated `~/.boulez/` config dir. Cold start: no migration from
  `cs` or `claude-squad`. See `PLAN-multi-repo.md`.
- See `PLAN.md` for the (completed) modularity plan. See `PLAN-multi-repo.md`
  for the multi-repo plan.
