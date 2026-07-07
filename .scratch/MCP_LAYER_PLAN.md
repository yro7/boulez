# MCP Layer — Implementation Plan

> Status: design, pending review before coding.
> Owner: Boulez
> Date: 2026-07-07

## Goal

Expose the Boulez fleet-control surface to an **orchestrator agent** as a
proper MCP server, so the agent calls **typed tools** (discovered via the
standard MCP `tools/list` handshake) instead of shelling out to the
low-level `boulez ctl` CLI and parsing JSON stdout.

This is decision **A** (tools at the conductor) from the design discussion.
It is **agent-neutral at the kernel level**: the MCP server is just a third
client of the kernel socket, alongside `boulez ctl` and the TUI. The kernel
remains the single source of truth and the sole enforcer of fleet rules
(topology, protected branches, caller attribution).

Non-goals (deferred):
- RPC transport to managed worker instances (decision **B**). Out of scope.
- A Pi-specific extension (`.pi/extensions/fleet.ts`). Pi is not an MCP
  client; if a Pi conductor ever needs tools, a second surface will be
  required. Not in this plan.

## Architecture

```
                 ┌──────────────────────────┐
                 │      kernel socket       │   (single source of truth,
                 │   ~/.boulez/ctl.sock     │    agent-neutral rules)
                 └──────────────┬───────────┘
                                │ JSON-RPC (newline-delimited)
            ┌───────────────────┼──────────────────────┐
            │                   │                      │
       ┌────┴────┐         ┌────┴─────┐          ┌─────┴─────┐
       │ TUI     │         │ boulez   │          │ boulez    │
       │ (client)│         │ ctl      │          │ mcp serve │  ← NEW
       └─────────┘         │ (one-shot│          │ (subprocess
                           │  client) │          │  per conductor)
                           └──────────┘          └─────┬─────┘
                                                        │ stdio
                                                        │ JSON-RPC (MCP)
                                                        │
                                                 ┌──────┴──────┐
                                                 │ orchestrator│
                                                 │ agent (LLM) │
                                                 │ e.g. Claude │
                                                 │ Code / Codex│
                                                 └─────────────┘
```

### Key property: stateless server, one source of logic

MCP's stdio transport is **one server subprocess per client** by protocol
design (Claude Code / Codex launch the server described in `.mcp.json`).
This is *not* a Boulez choice; it is how MCP stdio works. It is harmless
because the MCP server is **stateless**: it holds no fleet state in memory —
it forwards every tool call to the kernel socket. "N MCP servers" = N thin
pipes to the kernel, **not** N copies of the fleet logic. The logic stays
exactly once, in the kernel.

### Identity: inherited, not declared

The orchestrator does **not** know its own instance ID. The flow:

1. An orchestrator is spawned (TUI O key, `boulez ctl`, or any other
   spawner) → kernel allocates `<orch-id>`.
2. On spawn ack, the **spawner** (not the TUI specifically) writes `.mcp.json`
   into the orchestrator's control dir, containing
   `boulez mcp serve --as <orch-id>`.
3. The orchestrator agent starts, discovers `.mcp.json`, launches the MCP
   server subprocess.
4. The MCP server dials the kernel socket and sends `authenticate` with
   `<orch-id>` **once**, binding its connection. All subsequent syscalls it
   forwards are attributed to that orchestrator's plan — equivalent to
   `boulez ctl as <orch-id> ...`, but invisible to the agent.

**No TUI coupling.** Writing `.mcp.json` is the responsibility of the spawn
path's control-dir setup, not of any particular consumer. Concretely:
`orchestrator.WriteContextFile(id)` becomes
`orchestrator.WriteControlFiles(id)` and writes `ORCHESTRATOR.md` **and**
`.mcp.json` together. It is called by whoever acks an orchestrator spawn —
the TUI today, `boulez ctl` or an MCP tool tomorrow. The `app/` package only
calls the helper; it does not own the file contents.

Security note: `--as <id>` is visible in `ps`. Acceptable for local Boulez
(the control dir is written by the TUI, the agent never reads its own ID).
If MCP is ever exposed remotely, a token mechanism will be needed. Out of
scope.

### Tool surface (scope A)

Expose a **subset** of syscalls as MCP tools. Deliberately **exclude** `land`
(top-level-only, must not be offered to an orchestrator) and
`list_instances_full` (TUI-internal read path). Surface:

| MCP tool         | → kernel syscall      | kind   |
|------------------|-----------------------|--------|
| `list_instances` | `list_instances`      | read   |
| `get_instance`   | `get_instance`         | read   |
| `spawn_worker`   | `spawn_worker`        | mutate |
| `send_prompt`    | `send_prompt`         | mutate |
| `pause`          | `pause`               | mutate |
| `resume`         | `resume`              | mutate |
| `kill`           | `kill`                | mutate |
| `merge`          | `merge`               | mutate |

(`land` excluded — top-level only. An orchestrator lands via the human/TUI.)

### DRY story

The kernel wire structs are the **single source of truth** for tool schemas:

- `kernel.SpawnParams`, `kernel.mergeParams`, `kernel.landParams` carry
  `json` tags. The MCP server **imports `kernel`** and reuses these structs
  as the typed `args` of `mcp.AddTool`. The go-sdk generates the JSON schema
  **automatically** from struct tags (`json` + `jsonschema`). No parallel
  schema definition, no drift on field rename.
- Read-only syscalls (`list_instances`, `get_instance`, `pause`, `resume`,
  `kill`, `send_prompt`) use small inline param structs; these are defined
  **once** in the `mcp` package (they are MCP-specific projections, not
  kernel wire types — they have a different audience and stricter shape, so
  defining them in `mcp/` is correct SRP, not duplication).
- Error codes (`kernel.CodeProtectedBranch`, `CodeUnknownInstance`, …) are
  **referenced**, not redeclared. An MCP tool failure surfaces the kernel's
  `Error.Code` so the agent can branch on it.

## Package layout

New package `mcp/`:

```
mcp/
  server.go      // NewServer(as string, caller Caller) *Server; registers tools
  tools.go       // one func per tool: registerListInstances, registerSpawnWorker, ...
  caller.go      // Caller interface (the seam to the kernel socket)
  server_test.go // TDD: in-memory MCP client → fake Caller → assert syscall
```

### The seam (testability)

The MCP server must not dial a real socket in tests. Define:

```go
// Caller is the seam between the MCP server and the kernel control socket.
// Production: a socket-backed implementation (reuses kernel.CallSession).
// Tests: a fake that records emitted Requests.
package mcp

type Caller interface {
    // Call issues one authenticated syscall on behalf of the orchestrator
    // bound at NewServer time. The caller identity is already bound; params
    // must NOT carry a `caller` field.
    Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *kernel.ErrorInfo, error)
}
```

`kernel.ErrorInfo` is already exported (`transport.go`) — reuse it, do not
redefine. The socket implementation lives in `mcp/` (or `cli/`) and wraps
`kernel.CallSession` with a persistent authenticated connection held for
the server's lifetime.

> Open: `kernel.CallSession` is one-shot-per-call today (dials, sends,
> closes). The MCP server wants a **persistent** authenticated connection
> (authenticate once, reuse for the server's lifetime). Phase 7 will need
> either a `kernel.DialSession` that returns a long-lived session handle, or
> the MCP server holds its own `net.Conn` and speaks the line protocol
> directly. Decide at phase 7 based on what is least invasive to the kernel.
> Lean toward: MCP server owns a persistent `net.Conn` + a small
> `call(req) Response` helper, mirroring `CallSession` but reusing the
> connection. Keeps the kernel untouched (YAGNI on a new kernel API).

## Phases (TDD, each tranche leaves the repo building + green)

### Phase 0 — SDK spike (exploratory, may not commit)

Goal: confirm the go-sdk wire format and tool registration against a real
MCP client, in an isolated scratch program (not in the Boulez tree).

- `go get github.com/modelcontextprotocol/go-sdk@v1.6.1`
- Standalone `main.go` (in `/tmp` or `.scratch/`): one tool `echo`, stdio
  transport, exercised by an in-memory `mcp.NewClient`.
- Confirm: `initialize`, `tools/list`, `tools/call` round-trip; typed args
  struct → auto JSON schema.
- **Done when**: spike proves the handler signature
  `func(ctx, *mcp.CallToolRequest, Args) (*mcp.CallToolResult, any, error)`
  and `mcp.StdioTransport{}` work as documented. No Boulez code committed.

### Phase 1 — package `mcp/` + seam + empty server

Goal: skeleton that compiles, with the testability seam, exposing **zero**
tools.

Files:
- `mcp/caller.go` — `Caller` interface + a `fakeCaller` for tests (records
  `[]kernel.Request`, returns canned `json.RawMessage`).
- `mcp/server.go` — `NewServer(impl *mcp.Implementation, caller Caller) *Server`,
  wrapping `mcp.NewServer`. Holds the `Caller`. Exposes `Run(ctx, transport)`
  and a `Serve(stdio)` helper.
- `mcp/server_test.go` — in-memory client (`mcp.NewClient` over an in-proc
  transport) connects to `NewServer(...)` with a `fakeCaller`; asserts
  `initialize` succeeds and `tools/list` returns **empty**.

Done when: `go build ./... && go test ./mcp/...` green; `tools/list` empty.

### Phase 2 — tool `list_instances` (read path, TDD)

Goal: first real tool. Drives the schema-generation + result-rendering
conventions.

- `mcp/tools.go` — `registerListInstances(s *Server)`. Args struct
  (in `mcp/`, MCP-specific projection):
  ```go
  type listInstancesArgs struct {
      Kind   *string `json:"kind,omitempty"   jsonschema:"filter by instance kind: worker|orchestrator"`
      Status *string `json:"status,omitempty" jsonschema:"filter by status: running|ready|loading|paused"`
      Repo   string  `json:"repo,omitempty"   jsonschema:"filter by repo name"`
  }
  ```
  (Strings, not `session.Kind`/`session.Status`, to keep the schema
  self-describing for the LLM and the kernel's wire enum strings already
  exist — `cli.kindWire`/`cli.statusWire`. The MCP server translates
  string→kernel filter at the boundary.)
- Handler: build `kernel.Request{Method:"list_instances", Params:...}`, call
  `Caller.Call`, unmarshal `[]kernel.InstanceSummary`, render as
  `mcp.TextContent` JSON (compact, one object per line — easy for the LLM to
  read).
- Test: `fakeCaller` stubs `list_instances` → returns two summaries →
  client calls tool → assert the request the fake received has the right
  method + params, and the result text contains both instance IDs.

Done when: test green; schema auto-generated; no real socket touched.

### Phase 3 — tool `get_instance` (read path)

- `registerGetInstance`. Args: `{ ID string }`. Returns full
  `kernel.InstanceDetail` (summary + diff + log) as JSON text.
- Test: fake returns an `InstanceDetail` with a diff; assert rendering.

Done when: read tools complete; the agent can observe the fleet.

### Phase 4 — tool `spawn_worker` (mutate path, TDD)

Goal: first mutating tool; proves caller attribution works end-to-end
through the seam.

- `registerSpawnWorker`. Args = `kernel.SpawnParams` **reused directly**
  (DRY), minus the deprecated `Caller` field — define a local struct that
  embeds the relevant fields with the same `json` tags, or reuse
  `SpawnParams` and document that `caller` is ignored. Prefer: a local
  `spawnWorkerArgs` struct in `mcp/` with the **same json tags** as
  `SpawnParams` (so the wire contract is identical) but no `Caller` field —
  the MCP surface should not even mention caller.
- Handler: `Caller.Call("spawn_worker", params)` → `{"id": "..."}` → return
  the new ID as text.
- Test: fake receives `spawn_worker` request; assert params match; assert
  the result surfaces the returned ID. Also: assert the fake's request has
  **no** `caller` field (the MCP server must not inject one — identity is
  bound at the socket session).

Done when: mutate path proven; identity handled at the seam boundary.

### Phase 5 — remaining mutate tools

- `send_prompt` (args: ID, Prompt), `pause`/`resume`/`kill` (args: ID),
  `merge` (args = `kernel.mergeParams` sans `Caller`, same DRY approach as
  spawn).
- One test each, mirroring phase 2's shape. `merge` test additionally
  asserts a `PROTECTED_BRANCH` error code is surfaced as an MCP tool error
  the agent can branch on.

Done when: full tool surface wired and tested.

### Phase 6 — `boulez mcp serve` subcommand

Goal: real socket-backed `Caller`, wired into the CLI.

- `cli/mcp.go` — `NewMCPServeCmd() *cobra.Command`:
  - flags: `--as <instance-id>` (required).
  - Resolves `kernel.SocketPath()`, dials, sends `authenticate {instance_id:
    <as>, kind: orchestrator}` once, holds the connection.
  - Builds a `socketCaller` (persistent `net.Conn` + a `call(req) Response`
    helper mirroring `kernel.CallSession`'s loop but reusing the conn).
  - Constructs `mcp.NewServer(...)` with the socket caller, registers all
    tools, `server.Run(ctx, &mcp.StdioTransport{})`.
  - Auto-launches the daemon if the dial fails (mirror `cli/ctl.go`'s
    retry/launch behavior).
- Register under root: `boulez mcp serve`.
- Test: unit-test `socketCaller` against an in-process kernel
  (`kernel.New` + `net.Listen` unix socket) — authenticate, then a
  `list_instances` round-trip. (This is the one place a real kernel is
  exercised; it is the boundary test.)

Done when: `boulez mcp serve --as <id>` answers `tools/list` over stdio
against a real kernel. Manual smoke test: `echo` a JSON-RPC initialize +
tools/list into the command, confirm output.

### Phase 7 — Control-dir wiring + ORCHESTRATOR.md slimming

Goal: the orchestrator agent gets the MCP server automatically at spawn,
regardless of which spawner created it.

- `orchestrator/context.go`: rename `WriteContextFile` → `WriteControlFiles`,
  which writes **both** `ORCHESTRATOR.md` **and** `.mcp.json` into the control
  dir. `.mcp.json` content:
  ```json
  {
    "mcpServers": {
      "boulez": {
        "command": "boulez",
        "args": ["mcp", "serve", "--as", "<id>"]
      }
    }
  }
  ```
  Idempotent (same content per id), so safe to rewrite on every boulez restart
  (same property as `WriteContextFile` today).
- `app/app.go` `injectOrchestratorContext`: call `WriteControlFiles(id)`
  instead of `WriteContextFile(id)`. The TUI is one caller among potential
  others; it does not own the file contents. No new logic in `app/`.
- Slim `orchestrator/context.go` `ContextContent`: **remove** the "Your
  tools" / CLI doc / error-codes-table sections (the surface now lives in
  the MCP schemas, self-describing). Keep: role, two-level topology,
  "you are supervised, wait for a task", the fleet-snapshot pointer, and a
  one-line "your fleet tools are exposed via the `boulez` MCP server;
  call them directly" note.
- Update `orchestrator/fleet.go` `InjectionPrompt`: drop the
  `boulez ctl as <your-id>` instruction; replace with "call the boulez MCP
  tools". Keep the "refresh fleet state" instruction (now via the
  `list_instances` tool).
- Tests: extend `orchestrator` package tests to assert `.mcp.json` is
  written with the right id; assert `ContextContent` no longer mentions
  `boulez ctl`.

Done when: spawning an orchestrator writes `.mcp.json`; the agent's context
no longer references the CLI.

### Phase 8 — end-to-end smoke (manual or scripted)

Goal: prove a real conductor can drive the fleet.

Status: **documented, not automated** (requires a live daemon + a real
conductor). Procedure:

1. Start the daemon: `boulez daemon start` (or any TUI invocation, which
   auto-launches it).
2. Spawn an orchestrator with a conductor that supports MCP (Claude Code):
   launch the TUI with `-p claude`, press `O`. This writes `ORCHESTRATOR.md`
   + `.mcp.json` into the orchestrator's control dir.
3. Attach to the orchestrator's pane. Claude Code should discover the `boulez`
   MCP server via `.mcp.json` and list its tools.
4. Ask Claude to call `list_instances`. Confirm a syscall hits the kernel
   (visible in `boulez daemon log`) and the result renders in the pane.
5. (Stretch) ask Claude to `spawn_worker` a trivial task on this repo; confirm
   the worker appears in the fleet and the action is attributed to the
   orchestrator's plan.

Quick wire-level smoke (no conductor): pipe an MCP `initialize` into the
subcommand and confirm a valid JSON-RPC response:

```sh
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  | boulez mcp serve --as <orchestrator-id>
```

(Against a nonexistent id, the server exits with `authenticate:
UNKNOWN_INSTANCE: ...` — this proves the dial + authenticate path against
the live daemon.)

Done when: one full orchestrator→kernel round-trip via MCP succeeds with a
real conductor.

## Definition of done (whole layer)

- `go build ./... && go test ./...` green.
- `boulez mcp serve --as <id>` exposes all 8 tools over stdio MCP.
- An orchestrator spawned via the TUI (O key) gets `.mcp.json` automatically.
- `ORCHESTRATOR.md` no longer documents the CLI; the tool surface is
  self-describing via MCP schemas.
- No new coupling to any agent in `kernel/`, `session/`, or `program/`. The
  `mcp/` package is the only place that knows about MCP, and it imports
  `kernel` (toward stability), not the reverse.
- DRY: tool schemas derive from `kernel` wire structs (via struct tags), not
  hand-written.

## Open questions (resolve during phases)

1. **Persistent session API in kernel?** Phase 6. Lean: MCP server owns its
   own `net.Conn` + small helper, leaving `kernel.CallSession` untouched
   (YAGNI on a new public kernel API). Revisit if the line-protocol dance
   proves duplicative.
2. **Enum strings for `Kind`/`Status` in MCP args.** Phase 2 uses strings +
   translates at the boundary. Confirm the canonical wire strings (see
   `cli.kindWire`/`cli.statusWire`) and centralize the translation in one
   helper in `mcp/` to avoid scatter.
3. **`spawn_worker` args struct: reuse `SpawnParams` or project?** Phase 4.
   Prefer a local struct with identical json tags but no `Caller` field, so
   the MCP surface never mentions caller. Confirm the kernel ignores a
   missing `caller` field (it does — see `transport.go` comment).

## Decision note: keep `KindOrchestrator`, do not collapse to "instance + .mcp.json"

A natural reaction to the MCP layer is: *"an orchestrator is now just an
instance with `.mcp.json` + a special prompt — `KindOrchestrator` is
meaningless."* This was considered and **rejected**, for a concrete reason.

`Kind` is not decorative in the kernel. It is read to enforce rules:

- `transport.go:251` — an orchestrator cannot spawn an orchestrator
  (two-level topology, `ErrNestedOrchestrator`). Guard on the caller's Kind.
- `transport.go:283` — an orchestrator-spawned worker is recorded on the
  orchestrator's plan (resumability). Guard on
  `caller.Kind == KindOrchestrator`.
- `merge` — `RecordMerge` is reserved for orchestrators. Guard on Kind.
- `land` — top-level only; an orchestrator cannot land on a trunk. Guard on
  `!IsTopLevel()`.
- TUI view — `pinOrchestratorsFirst`. Guard on Kind.

Removing `KindOrchestrator` means either dropping these guards (recursion,
unattributed merges, trunk landings) or replacing them with a renamed
`IsOrchestrator` bool that is the same thing. No gain.

What the MCP layer *does* reveal is a real conceptual seam in the current
code: **(A) a control role** (instance authorized to spawn/merge/attribute
— the Kind, the guards) vs **(B) a configured supervisor agent** (`.mcp.json`
+ `ORCHESTRATOR.md` + injection prompt). Today (A) and (B) are coupled: only
a `KindOrchestrator` gets (B). In principle (B) is orthogonal to (A) — any
instance could discover tools via `.mcp.json`. But decoupling them has **no
current use case**: a worker given the fleet tools would have them refused by
the kernel's `IsWorker` spawn guard, so the tools would be inert. The
coupling (A)↔(B) is semantically justified as long as only controllers can
use controller tools.

Conclusion: keep `KindOrchestrator`. The MCP layer attaches to orchestrator
spawns (via `WriteControlFiles`) without redefining what an orchestrator *is*.
If a future use case requires a worker to carry fleet tools (e.g. a delegated
resolver), revisit then — YAGNI for now.
