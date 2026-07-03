# Report — cs2 control API hardening (PLAN-control-api-fixes.md)

> Final report for the 6-step control-API hardening plan. All 6 steps are
> implemented, each as a standalone atomic commit, `go build ./... &&
> go test ./...` green throughout. The dogfooding smoke test passes.

## Commits (in order)

| # | Commit | Step |
|---|--------|------|
| 1 | `3b0e432` `fix(log): keep ctl stdout pure JSON (no log-path line)` | 1 |
| 2 | `f0afb53` `fix(kernel): enforce host-current-branch merge guard (spec decision 7)` | 2 |
| 3 | `ffbfc3c` `fix(daemon): single-instance launch via lock file + socket wait` | 3 |
| 4 | `515a5b4` `feat(kernel): authenticate caller via transport session, not client params` | 4 |
| 5 | `e8dcaf4` `feat(session): Kind/Status marshal as strings, accept string|int` | 5 |
| 6 | `79d73e1` `feat(spawn): create branch if absent; add --branch-existing` | 6 |

(Commit `db6ef14` predates this plan — it is the Shape A plan-persistence
substrate step 4 builds on.)

## Summary of the 6 fixes

### 1. `cs2 ctl` stdout is strictly JSON (Fix #6)
`log.Close()` no longer prints `wrote logs to ...` to stdout. The
log-path-on-close behaviour is opt-in via `log.SetPrintPathOnClose`.
`cs2 ctl` output is now a single parseable JSON document —
`cs2 ctl list_instances | python3 -m json.tool` works without
post-processing.

### 2. Host-current-branch merge guard (Fix #3, spec decision 7)
The kernel refuses merges into the host repo's checked-out branch
(injected by the daemon at startup via `WithProtectedBranches`, resolved
once by `resolveHostProtectedBranches`). The Merger
(`session/git/merge.go`) defends `main`/`master` in depth *and* now also
treats the target repo's current branch as protected (computed before
checkout, so the post-checkout current branch doesn't self-allow). Merging
into the branch the user is standing on is now non-contournable.

### 3. Single-instance daemon launch (Fix #5)
`daemon.LaunchDaemon` takes an `O_EXCL` lock file (`~/.cs2/daemon.lock`)
with a PID; concurrent `ctl` storms launch exactly one daemon. A stale lock
with a dead PID is reclaimed. `cmd_ctl.go` replaced the blind `sleep` with
`daemon.WaitForSocket` (active poll, ~50 ms, ~3 s timeout) so a client that
lost the launch lock waits for the winner to bind before retrying.

### 4. Caller authenticated by the transport session (Fix #7 + #4, security)
The RPC `caller` param is now ignored server-side. Caller identity is
derived from a per-connection `ctlSession` bound via the new `authenticate`
syscall, which validates the instance exists and verifies its `Kind`. The
kernel uses the instance's **recorded** Kind, so a client can no longer lie
about its Kind to bypass the `WORKER_CANNOT_SPAWN` / `NESTED_ORCHESTRATOR`
guards. New `cs2 ctl as <id> <syscall>` subcommand authenticates then
issues a syscall on one connection (resolves finding #4: plan.json is now
reachable from the CLI — an orchestrator's `spawn_worker` calls are
attributed to it and recorded in its plan).

### 5. `Kind`/`Status` marshal as strings, accept string|int (Fix #8 + #2)
The wire now emits `"kind":"orchestrator"` / `"status":"paused"` (was
opaque ints 0/1/3). Unmarshal accepts both string and int, so a CLI
passing `--kind orchestrator` and a JSON-RPC client passing `"orchestrator"`
both work. `cmd_ctl.go`'s `kindInt`/`statusInt` conversion was removed —
the string flows straight through. Back-compat: old int-sending clients
still decode.

### 6. `spawn_worker --branch` creates the branch if absent (Fix #1)
`--branch X` now creates `X` from HEAD if it doesn't exist (the
orchestrator-friendly default — deterministic branch names without
pre-creating them). New `--branch-existing` flag restores the old behaviour
(requires a pre-existing branch, useful to resume work on one). A missing
required branch returns the new typed `git.ErrBranchNotFound`, mapped to
the `BRANCH_NOT_FOUND` wire code (not `INTERNAL`) — the kernel's
`spawn: %w` wrapping is unwrapped via `errors.As` in `kernelErrResp`.

The "create if absent" policy lives in one new helper,
`session/git.EnsureBranch(repo, branch, mustExist)`; the worktree layer
stays purely mechanical (no `branchMustExist` flag threaded through its
constructors). `setupFromExistingBranch` now returns the typed
`ErrBranchNotFound` too, so a restored worktree whose branch has vanished
is also typed.

## Dogfooding smoke test (final)

Isolated HOME under `/tmp/cs2-test-home`, throwaway repo under `/tmp`.
Built `/tmp/cs2` from the worktree.

- **Pure JSON stdout:** `cs2 ctl list_instances` → `[]` (no log line).
  Parseable by `python3 -m json.tool`. ✓
- **Kind/Status as strings:** `list_instances` shows `"Kind": "worker"`,
  `"Status": "running"`, `"Kind": "orchestrator"`. ✓
- **spawn --branch (absent) creates:** `spawn_worker --repo /tmp/test-repo6
  --branch newfeat --program bash --prompt "echo hi"` → `{id}`, and
  `git branch --list newfeat` confirms the branch was created. ✓
- **--branch-existing (absent) → BRANCH_NOT_FOUND:**
  `spawn_worker --branch ghost --branch-existing` →
  `{"code":"BRANCH_NOT_FOUND","message":"branch ghost not found locally or on remote"}`. ✓
- **`as` records the plan:** spawned an orchestrator, then
  `cs2 ctl as $ORCH spawn_worker ...` → worker created and recorded in
  `~/.cs2/orchestrators/$ORCH/plan.json` (`worker_ids` lists it). ✓

## Invariants pinned by tests

7. `cs2 ctl` stdout is a single JSON document, parseable without
   post-processing. (Step 1)
8. A syscall's caller comes from the transport (authenticated session),
   never from client params — a client cannot declare a worker identity to
   bypass a guard. (Step 4)
9. `merge` refuses the target repo's current branch at the kernel level,
   regardless of the client. (Step 2)
10. At most one daemon runs; auto-launch never spawns a second process.
    (Step 3)

Plus step 6's contract: a wrapped typed `git.ErrBranchNotFound` survives
`spawn: %w` and surfaces as `BRANCH_NOT_FOUND` over the wire (pinned by
`TestTransport_Spawn_BranchNotFound_ErrCode`).

## Status

All 6 steps complete. `go build ./... && go test ./...` green. The control
API is ready for a non-CLI consumer (Shape B: an LLM piloting the fleet) —
the wire is self-documenting (string enums), caller identity is
non-spoofable, and the spawn/merge surface behaves predictably for an
orchestrator.
