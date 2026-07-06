# Implementation Plan: Host-aware repo registry

## Problem (recap)

`repo.Registry` stores a **flat, deduplicated list of paths** with no host
dimension. A path can appear only once, so registering
`/root/Projets/teams-audio-scrapper` against `dev-machine` is silently
dropped (deduped against an existing local entry). Consequences:

- The TUI repo selector offers a remote repo bound to no host; the user is
  forced to re-type it as a free path every time, and that re-registration
  dedups away (so it never "sticks" for the remote host).
- `boulez ctl spawn_worker` / `boulez ctl as <id> spawn_worker` have **no
  `--host` flag**, so the wire `host` field is empty → `host.Lookup("")` →
  `LocalHost` → a local `git` invocation on a path that only exists on the
  remote machine → `failed to find Git repository root from path`.

## Key finding from the code

The wire already carries host. `kernel.SpawnParams.Host` exists and
`SpawnParams.toOptions()` does `host.Lookup(p.Host)` (→ `Local` when empty).
The TUI already passes the host through `m.pendingHost` → `opts.Host.Name()`
→ wire. So **the daemon does not need to consult the registry at spawn
time**: the host binding is chosen by the caller (TUI via the host selector,
CLI via `--host`) and sent authoritatively on the wire. The registry is a
**TUI convenience** (populating the selector + persisting the binding), not a
spawn-time resolver. This keeps the daemon decoupled from the registry and
matches the existing security model (the caller chooses; the daemon trusts
the wire, it does not infer).

So the task splits cleanly into:
- **A. Registry schema** — store `{path, host}`, allow the same path on
  different hosts, migrate the flat list in place with a backup.
- **B. TUI wiring** — register repos against the selected host (`pendingHost`)
  and offer only host-bound repos in the selector.
- **C. CLI `--host`** — expose the flag on `spawn_worker` (and thus `as`).

## Format (version 1)

`repos.json` becomes:

```json
{
  "version": 1,
  "entries": [
    {"path": "/Users/.../boulez", "host": "local"},
    {"path": "/root/Projets/teams-audio-scrapper", "host": "dev-machine"}
  ]
}
```

- `entries` preserves MRU order (global, as today — `Touch` moves an entry to
  the head of the whole list, not per-host). Keeping a single global MRU is
  the smallest change from the current behaviour and is what the tests assert.
- `host` is an opaque string stored verbatim; the canonical local value is
  `host.LocalAlias == "local"`. `repo` does **not** import `host` (layering:
  `repo` is a storage layer, `host` is a transport seam); the literal
  `"local"` is used inside `repo` only for the migration default, with a
  comment pointing at `host.LocalAlias`.
- Dedup key is now **`(path, host)`**, not `path` alone → the same path on
  two hosts is two entries (satisfies task point #2).

---

## Step 0 — tests first (TDD)

The `repo` package already has `registry_test.go` and `registry_edge_test.go`.
The plan is to update them to the new API and add migration/host-dimension
tests **before** the implementation, then make them green.

New/changed tests (file `repo/registry_test.go`):
- `Add(path)` → `Add(path, host)`; assertions gain a host arg.
- `TestRegistry_AddSamePathDifferentHostsDoesNotDedup`: `Add("/p","local")`
  then `Add("/p","dev-machine")` → `List()` has 2 entries.
- `TestRegistry_ContainsIsHostScoped`: `Contains("/p","dev-machine")` is false
  after only `Add("/p","local")`.
- `TestRegistry_TouchMovesEntryToHeadHostScoped`: `Touch("/p","local")` only
  moves the local entry, leaving a `dev-machine` entry for the same path in
  place.
- `TestRegistry_RemoveIsHostScoped`: `Remove("/p","local")` leaves the
  `dev-machine` entry.
- `TestRegistry_ListByHost`: returns only paths for the given host, in order.

New tests (file `repo/registry_migration_test.go`):
- `TestRegistry_MigratesFlatListToHostAware`: seed a `[]string` file →
  `List()` returns entries all with `host == "local"`; a
  `repos.json.pre-migration` backup exists with the original bytes; a second
  load reads the new format (idempotent — no second backup).
- `TestRegistry_MigrationPreservesOrder`: order of the flat list is kept.
- `TestRegistry_LoadNewFormatRoundTrip`: a hand-written v1 file loads as-is.
- `TestRegistry_CorruptFileYieldsEmpty` (moved/kept): corrupt JSON → empty,
  no backup written.
- `TestRegistry_RemoveOnMissingFileDoesNotCreateFile` / `Touch…` /
  `AddAfterCorruptFile` — kept, adapted to the new API (Add now needs a host).

## Step 1 — `repo/registry.go`: schema + API

Types:

```go
// Entry is one registered repo bound to a host. Host is an opaque alias
// ("local" for the machine running boulez, or an ssh alias). The same path
// on two hosts is two entries.
type Entry struct {
    Path string `json:"path"`
    Host string `json:"host"`
}

// registryV1 is the on-disk shape.
type registryV1 struct {
    Version int     `json:"version"`
    Entries []Entry `json:"entries"`
}
```

`load()` semantics:
1. Read file. Missing → `nil, nil` (cold start; preserves the
   "no-op Remove/Touch must not create the file" edge tests).
2. Unmarshal into `registryV1`. If `len(entries) > 0 || Version == 1` → new
   format: normalize each entry's `Host` to `"local"` when empty (defensive),
   return entries.
3. Else try unmarshal into `[]string` (old flat format). On success →
   **migrate**: build entries with `Host = "local"`, write a
   `<path>.pre-migration` backup of the original bytes, save the new format,
   return entries.
4. Corrupt (neither parses) → `nil, nil` (self-heal, no backup). Keeps the
   existing `ContainsOnCorruptFileReturnsFalse` / `CorruptFileYieldsEmpty`
   contract.

`save(entries []Entry)`: marshal `registryV1{Version:1, Entries: entries}`.
Keep the existing "create parent dirs" + atomic-enough write.

A small `migrateFlat(data []byte) ([]Entry, error)` helper does step 3,
including the backup (`os.WriteFile(r.path+".pre-migration", data, 0644)`,
best-effort: a backup failure does not block migration — log via `log`
package? No — `repo` is low-level; a failed backup is swallowed to keep the
storage layer dependency-free, matching the existing best-effort tone). The
backup is written only once: if `<path>.pre-migration` already exists, do not
overwrite it (so a re-load after migration doesn't clobber the original
backup).

Public API (signature changes ripple to the callers listed in Step 2/3):

```go
func (r *Registry) List() ([]Entry, error)
func (r *Registry) ListByHost(host string) ([]string, error) // paths for host, MRU order
func (r *Registry) Contains(path, host string) bool
func (r *Registry) Add(path, host string) error
func (r *Registry) Remove(path, host string) error
func (r *Registry) Touch(path, host string) error
```

- `resolveAbsolute(path)` stays (local absolute resolution). Note: for a
  remote path, `filepath.Abs` resolves against the *local* cwd, which is
  meaningless — but the registry stores the path as the user typed it under a
  known host, and the host's `ResolveRepoPath` (LocalHost abs, SSHHost
  passthrough) is what normalizes at spawn time. So the registry should
  **not** absolutize remote paths. Decision: `Add` absolutizes only when
  `host == "local"` (mirroring `LocalHost.ResolveRepoPath`); for a non-local
  host the path is stored verbatim (mirroring `SSHHost.ResolveRepoPath`
  passthrough). This keeps the registry's normalization consistent with the
  transport that will consume the path, and avoids mangling `~`-relative or
  remote-absolute paths. `Contains`/`Remove`/`Touch` use the same
  host-conditional normalization so the contract stays symmetric (existing
  `TestRegistry_RemoveRelativeMatchesAbsoluteAdd` is kept for the local
  case).
- `ListByHost("")` treats empty as `"local"` (defensive; matches
  `host.Lookup("")` → Local).
- Dedup in `Add` compares normalized `(path, host)`.
- `Touch` finds the entry by `(path, host)` and moves it to the head.
- `Remove` filters by `(path, host)`; "only persist if something changed"
  preserved (keeps `RemoveOnMissingFileDoesNotCreateFile`).

## Step 2 — TUI wiring (`app/`)

### `app/repo_select.go`
- `openRepoSelector`: replace
  `repos, _ := m.repoRegistry.List()` with
  `repos, _ := m.repoRegistry.ListByHost(h.Name())` where `h` is the
  already-computed `m.pendingHost` (or `host.Local`). The selector now
  starts with host-bound repos; `filterRepos(repos, h)` still probes
  existence over SSH (keeps the "drop deleted repos" behaviour; just a
  smaller probe set). Local users see no change.
- `handleRepoSelectState` submit path: capture `h := m.pendingHost; if h == nil
  { h = host.Local }` (already done today). Change:
  - `m.repoRegistry.Add(selected)` → `m.repoRegistry.Add(selected, h.Name())`
  - `m.repoRegistry.Touch(selected)` → `m.repoRegistry.Touch(selected, h.Name())`
- `applyRepoDeletions`: `m.repoRegistry.Remove(path)` →
  `m.repoRegistry.Remove(path, m.pendingHost.Name())` (guard nil → local, same
  as elsewhere). `pendingHost` is set for the whole repo-select state.

No new TUI screen: the host is already chosen before the repo selector opens
(host selector → repo selector), so "pick/confirm the host when adding a
repo" (task #5) is satisfied by binding to `pendingHost`. The only gap was
that the binding was never *stored* — now it is.

### `app/host_select.go`
No change. The host selector already populates `pendingHost`; the registry
(host) is untouched.

### `app/preset_select.go`
No change. Presets already carry `host` and go through `host.Lookup(p.Host)`
directly; they bypass the repo registry (a preset is an explicit recipe, not
a registry mutation — already documented).

### `app/app_test.go`
Update `repo.NewRegistryAt(...)` usages only if they call the mutated
methods; otherwise the construction is unchanged. Audit on edit.

## Step 3 — CLI `--host` (`cli/ctl.go`)

`NewCtlSpawnCmd`:
- Add `var hostFlag string` + `cmd.Flags().StringVar(&hostFlag, "host", "",
  "execution host: an ssh alias, or empty/local for the local machine")`.
- In `RunE`, when `hostFlag != ""`, set `params["host"] = hostFlag`.
- Update the `--help` example to show `--host dev-machine`.

`as` path: `runCtlAs` → `buildCtlSub("spawn_worker")` returns
`NewCtlSpawnCmd()`, which now has `--host` registered. Because
`buildCtlRequest` runs the subcommand's `RunE` against the capture hook,
`--host` flows into the captured `SpawnParams` automatically. **No extra
code** is needed for `as` — verify with a test that
`boulez ctl as <id> spawn_worker --repo /r --host dev-machine …` sends
`"host":"dev-machine"` on the wire.

The wire → `SpawnParams.toOptions()` → `host.Lookup(p.Host)` already turns
`"dev-machine"` into an `SSHHost`. So once `--host` reaches the params, the
remote spawn works end-to-end (this is why the daemon needs no registry
lookup).

## Step 4 — `cli/repo_import.go`

`repo-import` discovers repos from **local** IDE state, so every imported
path is bound to the local host:
- `reg.Contains(f.Path)` → `reg.Contains(f.Path, host.LocalAlias)`
- `reg.Add(f.Path)` → `reg.Add(f.Path, host.LocalAlias)`

Add `import "github.com/yro7/boulez/host"` to `cli/repo_import.go`. No cycle
(`cli` already imports `daemon`/`kernel`; `host` is lower-level).

`cli/repo_import_test.go`: `reg.Add(known)` → `reg.Add(known, "local")`;
`formatDryRunSummary`'s `reg.Contains(f.Path)` → `reg.Contains(f.Path,
"local")`.

## Step 5 — (optional, adjacent) `EnsureBranch` executor fix

Separate from the reported bug (the failing command has no `--branch`, so
`EnsureBranch` is never reached), but it is a latent bug on the remote spawn
path: `app/spawn.go` calls `git.EnsureBranch(opts.Repo, …)` which uses
`cmd.MakeExecutor()` (local). For a remote repo **with** `--branch`, this
runs `git -C /root/... branch` locally → fails.

Proposed (own commit, clearly separable):
- `git.EnsureBranch(repo, branch, mustExist)` →
  `git.EnsureBranch(ce cmd.Executor, repo, branch, mustExist bool)`.
- `app/spawn.go`: capture `h := opts.Host; if h == nil { h = host.Local }`
  early (also unifies the `SetHost` path), then
  `git.EnsureBranch(h.Executor(), opts.Repo, opts.Branch, opts.BranchMustExist)`.
- Update `session/git/branch_test.go` (5 tests) to pass `cmd.MakeExecutor()`.

**Drop this step if you want the tightest possible scope**; the core fix
(Steps 0–4) is sufficient for the reported failure.

## Step 6 — verification

1. `go build ./... && go test ./...` green.
2. Manual migration test: copy the live `~/.boulez/repos.json` (flat list)
   into a temp registry, `List()` → all entries `host=="local"`,
   `repos.json.pre-migration` created with the original bytes, second `List()`
   reads the new format without rewriting the backup.
3. TUI: pick `dev-machine` host → repo selector shows only repos bound to
   `dev-machine`; type `/root/Projets/teams-audio-scrapper` as a free path →
   validate (probed over SSH) → register as `(path, "dev-machine")` → spawn
   succeeds (remote tmux/git over SSH, as the daemon log already shows working
   for other remote sessions). Restart the TUI → the repo reappears under
   `dev-machine` and does **not** appear under `local`.
4. CLI: `boulez ctl as <id> spawn_worker --repo /root/Projets/teams-audio-scrapper
   --host dev-machine --program pi --prompt "test"` → spawns a remote instance
   (no more `failed to find Git repository root from path`).
5. Same path, two hosts: register `/root/Projets/teams-audio-scrapper` under
   both `local` (it'll fail to validate locally, but the entry persists if
   added freely) and `dev-machine` → `List()` has 2 entries; removing one
   leaves the other.

## Commit shape (tiny, reviewable)

1. `repo: migrate registry to host-aware {path, host} schema + tests` (Steps 0–1)
2. `app: bind repo registrations to the selected host (TUI)` (Step 2)
3. `cli: add --host to spawn_worker (and `as`)` (Step 3)
4. `cli: repo-import binds imported repos to local` (Step 4)
5. *(optional)* `spawn: route EnsureBranch through the host executor` (Step 5)

## Files touched

- `repo/registry.go` — schema, migration, API.
- `repo/registry_test.go` — API updates.
- `repo/registry_edge_test.go` — API updates (Add gains host).
- `repo/registry_migration_test.go` — new.
- `app/repo_select.go` — `ListByHost`, host-scoped Add/Touch/Remove.
- `cli/ctl.go` — `--host` flag.
- `cli/repo_import.go` + `cli/repo_import_test.go` — local host binding.
- *(optional)* `session/git/branch.go` + `session/git/branch_test.go` +
  `app/spawn.go` — EnsureBranch executor.

## Non-goals (explicitly out of scope)

- Daemon-side registry lookup at spawn (the wire is authoritative; the daemon
  stays decoupled from `repo`).
- Auto-resolving a host when `--host` is omitted (would couple the daemon to
  the registry and is ambiguous when a path is on several hosts). Omitting
  `--host` keeps the current explicit `local` default.
- Per-host MRU ordering (global MRU is preserved).
- Migrating the two obviously-remote paths already in the live `repos.json`
  to `dev-machine`/`marseilleFreebox` automatically — the migration cannot
  know which host a bare path belongs to, so all bare paths become `local`
  (per the handoff) and the user re-registers remote ones on first use.
