# CS2 Hierarchy Inversion — Plan

**Goal:** make boulez what it claims to be: a long-lived **kernel (service)** that owns
the fleet, with the **TUI and `boulez ctl` as ephemeral clients** over the control
socket. Today the TUI is the process parent, the kernel is a sidecar it spawned and
then ignores, and the TUI persists fleet state directly to `session.Storage` — a
double-writer that desyncs the kernel from disk. This plan finishes the inversion that
was started (and abandoned at ~10%) in `app/land_caller.go`.

**Scope:** invert the authority, kill the double-writer, make the daemon a first-class
OS service with no dependency on any user repo. **Out of scope:** rewriting the kernel
(it is already clean), rewriting the tmux/git plumbing (it works), greenfield rewrite,
plugin / hot-reload framework, removing bubbletea.

**North-star invariant — stated and enforced:**
> The daemon process is the single writer over fleet state. No process other than the
> daemon constructs a `*kernel.Kernel` or touches `session.Storage` for writes. The TUI
> and `boulez ctl` are pure clients of the control socket. The daemon and the TUI have
> **no repo / workspace of their own** — they operate above the repo layer. Repos are
> a property of worker instances, never of the supervisor or its viewer.

---

## Current state (verified 2026-07-04)

- `kernel.New` is called in exactly **one** place: `daemon/daemon.go:53`. Good —
  the kernel is already a single instance.
- The daemon is spawned by the TUI at `main.go:64` (`daemon.LaunchDaemon()`), detached
  via `Setsid`. It is "canonical and long-lived" per comments but actually only ever
  launched by the TUI, and the TUI then ignores it for every mutation.
- The TUI loads its own fleet from `session.Storage` directly (`app/app.go:209`), not
  from the kernel. It persists directly at **7 sites**: `app/app.go:312, 436, 470, 901,
  909, 924, 1544`. This is the double-writer.
- The only TUI→kernel bridge is `app/land_caller.go` (~70 LOC) for the `land` syscall.
  spawn / pause / resume / kill / orchestrator all bypass the kernel.
- The `IsGitRepo` cwd gate from upstream CS has already been removed from boulez's
  `main.go` — the TUI no longer hard-requires a repo at boot. Good base.
- **Residual debt:** `daemon.resolveHostProtectedBranches` reads `os.Getwd()` to derive
  "the branch the user is standing on." This is the **only** remaining coupling between
  the daemon and the user's filesystem, and it is meaningless for a service process
  (launchd/systemd run with no meaningful cwd). Killing it is part of this plan.
- **Bug A (desync):** TUI save clobbers kernel in-memory state. The orchestrator spawned
  via `spawnOrchestrator` is written to `state.json` but never registered with the
  running kernel → `UNKNOWN_INSTANCE` for its own kernel. **Tolerated during the
  migration** — it disappears by construction once the TUI stops being a writer.
- **Bug B (kill zombie):** `kernel.Kill` calls `inst.Kill()` + persists but never
  `instStore.remove(id)`. Killed instances stay `running` in memory; the poll loop
  hammers them with `UpdateDiffStats` (the `tmux.go:278` error storm).
- **Bug C (auth `as` swallowed):** `runCtlAs` sends `[authenticate, syscall]` and
  `rawCtlSession` only prints the last response. A failed `authenticate`
  (`ErrUnknownInstance`) is silently dropped, the session stays top-level, and the
  syscall runs unattributed — bypassing the recursion guard and plan recording.

Bugs B and C are fixed in the hardening phase, not patched in isolation — patching them
against the current architecture would mean writing code that is then rewritten when
the inversion lands.

---

## Locked decisions

### D1 — Protected branches: per-repo, explicit (Option A)

The daemon has no cwd, no repo, no workspace. Branch protection cannot be derived from
"where the user is standing" because **the daemon does not stand anywhere.** Protected
branches are declared per-repo in an explicit store (`repos.json` extension or a new
`protected.json`), read at daemon boot and on `SIGHUP`. The cwd-based
`resolveHostProtectedBranches` is deleted. This is the clean separation: repos belong
to worker instances; the supervisor knows them only as named, protected targets.

### D2 — Command shape: `boulez` = `boulez tui`, daemon auto-started

- `boulez` (and `boulez tui`) — the TUI. Connects to the daemon's control socket. If the
  socket is absent, **auto-starts the daemon** and waits for the socket (best-effort).
  **If the daemon fails to come up — for any reason: crash, missing binary, bad config
  — the TUI does not start.** It prints the failure (with the tail of the daemon log
  and the path to `boulez daemon log`) and exits non-zero. There is no degraded TUI mode
  over a broken daemon.
- `boulez ctl` — unchanged, one-shot client. Auto-launches the daemon the same way the
  TUI does (existing behavior in `rawCtlSession`), with the same "fail loudly if the
  daemon won't come up" contract.
- `boulez daemon run` — foreground daemon (dev / debug). The canonical entrypoint that
  the service unit and the auto-start both invoke under the hood.
- `boulez daemon start|stop|status|log` — manage the background daemon (start = detached
  via launchd/systemd if installed, else `nohup`/`Setsid`).
- `boulez daemon install|uninstall` — write/remove the launchd plist or systemd unit.

No `auto_start_daemon` config flag: auto-start is unconditional. The escape hatch is
`boulez daemon run` in the foreground to see what is wrong.

---

## Phases

Phases are ordered by dependency. Each phase is independently shippable and ends with
the fleet in a working state. Commits within a phase are tiny and reviewable.

### Phase 1 — `boulez daemon` as a first-class command

**Why first:** separates the daemon's lifecycle from the TUI at the command level,
which is the prerequisite for installing it as a service and for the TUI becoming a
client.

- **C1.1 — Promote `--daemon` to `boulez daemon run`.** New `cmd_daemon.go`; moves
  `daemon.RunDaemon` invocation out of the root `RunE`. Keep `--daemon` as a hidden
  alias for one release (back-compat for any in-flight process / scripts).
- **C1.2 — `boulez daemon start|stop|status|log`.** `start` launches the daemon detached
  (reuse `LaunchDaemon` + the existing O_EXCL lock). `stop` reuses `StopDaemon`.
  `status` probes the socket + reads `daemon.pid`. `log` prints the tail of the
  boulez log.
- **C1.3 — `boulez` / `boulez tui` auto-start contract.** At TUI boot, probe the socket; if
  absent, call `LaunchDaemon` then `WaitForSocket` (exists already). On timeout, **do
  not start the TUI**: print the daemon log tail and exit non-zero. Remove the current
  silent "proceed with whatever storage holds" fallback. The TUI is a viewer of the
  kernel; no kernel, no viewer.
- **C1.4 — Stop spawning the daemon from the TUI's `RunE`.** The TUI's responsibility
  becomes "ensure the daemon is reachable" (probe + auto-start + fail-loud), not "be
  the daemon's parent." The daemon's parent is launchd/systemd (after Phase 2) or
  `Setsid`-detached (during transition).

**Acceptance:** `boulez daemon run` runs the kernel in the foreground; `boulez daemon start`
runs it detached; `boulez ctl list_instances` works against either; the TUI refuses to
start if the daemon cannot come up.

### Phase 2 — Daemon as OS service + no repo/workspace

**Why merged:** the two are the same change — making the daemon a real service **is**
making it independent of the user's filesystem. The cwd-derived branch protection is
the last filesystem coupling; it dies here.

- **C2.1 — Per-repo protected-branch store.** Extend `repos.json` (or add
  `~/.boulez/protected.json`) mapping `repoPath -> []branch`. `boulez daemon protect
  <repo> <branch>` / `unprotect` / `list-protected` subcommands. Loader reads at boot
  and on `SIGHUP`.
- **C2.2 — Kernel reads the explicit protected set; cwd path deleted.** Replace
  `resolveHostProtectedBranches` (cwd-based) with the store. `WithProtectedBranches`
  is fed from the store at boot and reloaded on `SIGHUP`. Delete
  `resolveHostProtectedBranches` and its call site. Conventional `main`/`master`
  guard in the Merger is unchanged (defense in depth).
- **C2.3 — `boulez daemon install|uninstall` (macOS).** Write
  `~/Library/LaunchAgents/ai.smtg.boulez.plist` (`RunAtLoad`, `KeepAlive`,
  `ProgramArguments` = `<exe> daemon run`). `install` writes the plist and
  `launchctl bootstrap gui/<uid>`; `uninstall` `bootout`s and removes.
- **C2.4 — `boulez daemon install|uninstall` (Linux).** Write
  `~/.config/systemd/user/boulez.service` (`Restart=on-failure`,
  `WantedBy=default.target`). `systemctl --user daemon-reload` + `enable --now`.
- **C2.5 — Dev fallback.** If neither launchd nor systemd is present,
  `boulez daemon start` documents the `nohup boulez daemon run &` form. No custom
  supervisor.
- **C2.6 — Audit: the daemon has no filesystem-of-the-user dependency.** After C2.2,
  grep the daemon package for `os.Getwd`, `os.Getenv("PWD")`, and any repo path
  derived from the process rather than from instance data. Add a test that the daemon
  boots and serves `list_instances` with cwd set to `/` (a directory that is not a git
  repo and not writable).

**Acceptance:** after `boulez daemon install`, a reboot brings the kernel back up; `boulez
ctl list_instances` works against it; the daemon boots cleanly from cwd `/`; a merge
into a declared-protected branch is refused with `PROTECTED_BRANCH`; `SIGHUP` reloads
the protected set.

### Phase 3 — TUI becomes a kernel client (the inversion)

**Why now:** after Phase 2 the daemon is the persistent, repo-free authority. The TUI
can now be reduced to a viewer of that authority. This is the phase that kills the
desync by construction.

- **C3.1 — `app/` fleet client wrapper.** A small file in `app/` (mirroring
  `land_caller.go`'s shape): `socketFleetClient` exposing `list_instances`,
  `spawn_worker`, `pause`, `resume`, `kill` over the control socket. One seam, one
  file.
- **C3.2 — TUI reads the fleet from the kernel.** Replace `storage.LoadInstances()`
  at `app/app.go:209` with a `list_instances` syscall. The TUI keeps a **read-only
  cache** of the fleet, refreshed from `list_instances` on the existing daemon poll
  cadence and on every mutation ack. The TUI owns the view, not the truth.
- **C3.3 — Route spawn through the kernel.** `spawnOrchestrator` (O key) and worker
  spawn (n key) issue `spawn_worker` syscalls (`Kind=KindOrchestrator` / `KindWorker`)
  instead of `session.NewInstance` + `inst.Start` + `SaveInstances`. The syscall
  returns the new ID; the TUI re-reads the fleet (C3.2) to pick it up.
- **C3.4 — Route pause / resume / kill through the kernel.** The TUI's keybindings
  call syscalls instead of `inst.Pause()` etc. directly. Kill is the important one: it
  must go through the kernel so the `remove` from the hardening phase (Phase 4) takes
  effect on the kernel's copy.
- **C3.5 — Delete the 7 direct `SaveInstances` calls** in `app/app.go` (lines 312, 436,
  470, 901, 909, 924, 1544) and the `LoadInstances` at boot (line 209). The TUI no
  longer imports `session.Storage` for writes.
- **C3.6 — Audit: the TUI has no repo / workspace of its own.** Confirm every
  `repoPath` reference in `app/app.go` is an instance's repo (e.g. `selected.Path`),
  never the process cwd. The TUI never derives state from where it was launched. Add a
  test that boots the TUI's home model from a non-repo cwd and the fleet view is
  populated purely from the kernel.

**Acceptance:** grep shows zero `SaveInstances`/`LoadInstances` in `app/`; spawning
from the TUI makes the instance immediately visible to `boulez ctl list_instances`;
killing from the TUI removes it from `boulez ctl list_instances`; the TUI boots from a
non-repo cwd and displays the kernel's fleet. The desync is gone by construction.

### Phase 4 — Hardening

**Why last:** these are correctness fixes that only fully make sense once the kernel is
the unambiguous single writer. Doing them earlier would mean writing them against the
double-writer and rewriting them after the inversion.

- **C4.1 — Fix Bug B (kill zombie).** In `kernel.Kill` (`kernel/kernel.go`), after
  `inst.Kill()` succeeds, take `k.mu`, call `k.instStore.remove(id)`, then persist. Add
  a test: spawn (fake spawner) → kill → assert not in `LiveInstances()` and not
  persisted. Also mutes the `tmux.go:278` log storm (the poll loop no longer visits
  killed instances).
- **C4.2 — Fix Bug C (auth `as` swallowed).** In `cmd_ctl.go: runCtlAs` /
  `rawCtlSession`, inspect the `authenticate` response. If it is an error, print it and
  exit non-zero **without** issuing the syscall. Add a test: `as <unknown-id> spawn`
  → expect `UNKNOWN_INSTANCE`, expect no spawn side-effect.
- **C4.3 — Single-writer enforcement at compile time.** Move `Storage` write methods
  behind an unexported symbol on the kernel package, so `app/` cannot reach
  `SaveInstances` even if it tried. `session.Storage` remains for the kernel's use
  only. (Belt-and-braces after C3.5; C3.5 is the behavioral fix, C4.3 makes it
  unbreakable.)
- **C4.4 — Daemon reconciliation on boot.** After `loadLocked`, probe tmux liveness
  for each loaded instance. Instances whose tmux session is gone are marked dead (not
  `running`). This closes the "ghost `running` from the morning" symptom at the source.
  Only demote when tmux definitively reports the session absent — never on a timeout
  (a slow instance is not a dead one).
- **C4.5 — Log hygiene.** Gate the `tmux.go:278` "error capturing pane content" behind
  the existing `everyN` throttle so a transiently-unreachable instance does not spam per
  tick. Mostly moot after C4.1/C4.4, but cheap.
- **C4.6 — Deprecate `--daemon` flag.** Remove the hidden alias now that
  `boulez daemon run` exists. One release after C1.1.

**Acceptance:** `app/` does not compile if it reaches for storage writes; a daemon
restart after a tmux crash shows the crashed instance as dead, not running; the log is
quiet for dead instances; `as <bogus> spawn` errors loudly with no spawn side-effect.

---

## Risk register

| Risk | Likelihood | Mitigation |
|---|---|---|
| TUI loses responsiveness by round-tripping every mutation through the socket | Med | The socket is local unix; mutations are rare (human cadence). Poll `list_instances` at the existing daemon poll interval, not per keystroke. |
| Losing the TUI's in-memory list breaks bubbletea's model (it expects to own its state) | Med | Keep a read-only cache in the TUI, refreshed from `list_instances` on a timer + on every mutation ack. The TUI owns the *view*, not the *truth*. |
| Existing users have `--daemon` in scripts / launchd plists | Low | Keep `--daemon` as a hidden alias for one release (C1.1) before C4.6 removes it. |
| launchd/systemd unit paths differ across distros | Low | Start with the two well-known user paths (macOS LaunchAgent, Linux user systemd). Don't over-engineer; document `nohup` fallback (C2.5). |
| Per-repo protected branches surprise users who relied on cwd protection | Med | Migration: on first `boulez daemon install`, seed `protected.json` with the current cwd's branch for any registered repo that matches. One-time, opt-out. Documented in the install command's output. |
| Reconciliation (C4.4) marks a slow instance as dead | Low | Only demote when tmux reports the session gone (definitive), never on a timeout. |
| Bug A (orchestrator `UNKNOWN_INSTANCE`) is tolerated during the migration and bites dogfooding | Med | Acceptable per decision: the dev target is boulez itself, not a production orchestrator. If dogfooding an orchestrator becomes necessary mid-migration, C0.3-equivalent (route `spawnOrchestrator` through the kernel) can be cherry-picked out of Phase 3 as a one-off. |
| Auto-start masks a crashing daemon from the user | Low | By decision D2: the TUI does not start if the daemon won't come up. The failure is surfaced with the daemon log tail, not swallowed. |

---

## Sequencing summary

```
Phase 1 (boulez daemon command)      ── daemon lifecycle detached from TUI
   │
   ▼
Phase 2 (OS service + no repo)    ── daemon persistent, repo-free (depends D1, D2)
   │
   ▼
Phase 3 (TUI = kernel client)     ── the inversion; desync gone by construction
   │
   ▼
Phase 4 (hardening)               ── single-writer enforced, reconciliation, bugs B/C
```

Phase 3 depends hard on Phase 2: the TUI cannot stop being a source of truth until the
daemon is the persistent, repo-free authority it defers to.

---

## What this plan deliberately does NOT do

- **No Phase 0 / stop-the-bleeding.** Bugs A/B/C are symptoms of the double-authority;
  patching them against the current architecture would mean writing code that is then
  rewritten when the inversion lands. They are fixed in Phase 4 against the correct
  architecture.
- **No kernel rewrite.** `kernel/` is already the deep module this project wants.
- **No tmux/git plumbing rewrite.** `session/` is inherited debt but debugged. Its
  cosmetic bugs (the log spam) are addressed in C4.5, not by rewriting.
- **No plugin / hot-reload framework.** Runtime flexibility lives in the orchestrator
  agent (an LLM driving the 8 syscalls), not in the kernel. The kernel should be
  boring, stable, correct.
- **No greenfield repo.** The kernel, transport, spawner seam, merger seam are reusable
  as-is. A new repo would re-acquire the same tmux/git bugs for no gain.
- **No bubbletea removal.** The TUI stays; it just stops being an authority. (A future
  plan could replace `ui/` with a thinner view, but that is orthogonal to the inversion
  and not required to fix the desync.)

---

## Open questions for the human

1. **Orchestrator auto-respawn on daemon restart:** out of scope here, but the plan
   store (`kernel/plan.go`) already supports it. Defer to a follow-up?
2. **Multi-host / remote execution:** the `hosts.json` (`dev-machine`,
   `marseilleFreebox`) suggests remote hosts are coming. This plan is local-only;
   remote execution is a separate epic that the `host.Host` seam already anticipates.
3. **Migration seeding for protected branches (risk register):** seed
   `protected.json` from the current cwd on first `boulez daemon install`, or start
   empty and require explicit `boulez daemon protect`? Default: seed, opt-out.
