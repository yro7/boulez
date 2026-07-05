# Boulez

> One conductor for all your coding agents.
>
> Boulez is a kernel and daemon that **orchestrates multiple AI coding agents**
> (Claude Code, Codex, Pi, Gemini, Aider, …) running concurrently, each in its
> own isolated git worktree — **local or over SSH** — with a TUI to supervise
> the whole fleet from one place.

Before working here, read **[AGENTS.md](./AGENTS.md)** for the project's goals,
non-negotiable rules, and code philosophy.

## Origin

Boulez is derived from [claude-squad](https://github.com/smtg-ai/claude-squad)
(upstream commit `5a604f7`, v1.0.19). It is **not** the upstream project and is
not affiliated with `smtg-ai`. The original `AGPL-3.0` license is preserved
(see [LICENSE.md](./LICENSE.md)); attribution to the upstream authors is kept in
the git history and in this notice.

What changed since the fork:

- Ships as a standalone `boulez` binary (separate config dir `~/.boulez/`).
- **Kernel + daemon architecture**: the TUI is only one consumer of a daemon
  that owns the kernel / control authority. `boulez ctl` is a thin client.
- **Modular agent support** via a `program.Adapter` seam: adding a new agent
  (Pi, Codex, Amp, …) is one file under `program/` + one `Register` line, with
  no edits to the tmux core, TUI, or daemon.
- **Multi-repo orchestration**: the TUI centralizes instances running across
  several different repositories.
- **Multi-env (SSH)**: an instance's whole environment (worktree, tmux, agent)
  can run on a remote machine over SSH while you supervise it locally.
- A small Pi ↔ boulez ready-signal bridge (see `extensions/pi-boulez.ts` +
  `program/pi.go`).

---

## Repo registry & one-shot IDE import

Boulez keeps a registry of known repositories (`~/.boulez/repos.json`) used to
pre-populate the repo selector when creating an instance. Repos are added to
the registry automatically as you use them; you can also **import them in a
one-shot, manual pass** from the IDEs you already use.

```bash
# Preview (read-only) what would be imported, without writing:
boulez repo-import --dry-run

# Import: scan all installed VS Code-family IDEs, keep only git repos,
# add the new ones to the registry:
boulez repo-import

# Restrict the scan to a single IDE:
boulez repo-import --ide cursor
```

Supported IDEs (all VS Code-family forks sharing the same `storage.json`
layout): `vscode`, `cursor`, `windsurf`, `antigravity`, `vscodium`, `pearai`,
`void`, `trae`.

This is a **one-shot, manual** import — boulez never reads IDE state
automatically, so a format change in an IDE's `storage.json` never affects
normal operation. The IDE parsing is isolated in the `ideimport/` package.

---

## Remote instances (SSH)

Boulez can run an instance's whole environment (git worktree, tmux session,
agent) on a **remote machine** over SSH while you supervise it from the local
TUI. A single dashboard can then span several machines — e.g.
`(A, local)`, `(A, gpu-box)`, `(B, gpu-box)`.

### How it works

Every command, filesystem operation, and PTY the instance needs is routed
through the system `ssh` binary, reusing your existing SSH config
(`~/.ssh/config`, agent, keys). Boulez never stores credentials. An instance on
host `dev-machine` runs `ssh dev-machine git ...`, `ssh dev-machine tmux ...`,
and attaches via `ssh -t dev-machine tmux attach-session -t <name>`.

### Picking a host

When creating an instance (`n` / `N`), the first screen is the **host
selector**. `local` (this machine) is always listed first; any SSH aliases
you have used before follow; you can also type a new alias as free text — it
is remembered for next time (stored in `~/.boulez/hosts.json`).

The alias must resolve through your SSH config / known hosts. Boulez treats it
as opaque — user, port, and key resolution are ssh's job.

### Preconditions on the remote host

The remote machine must have installed:

- **tmux** (boulez drives a remote tmux session), and
- **the agent binary** you launch (e.g. `claude`, `codex`, `aider`, …),
  reachable on the remote `PATH`.

Boulez creates the worktree under `~/.boulez/worktrees` on the remote host;
the `~` is expanded by the remote shell, so it lands in the remote user's
home.

### Performance: SSH multiplexing

By default each operation opens a new SSH connection. For a smoother
experience — especially with several remote instances — enable SSH
multiplexing in `~/.ssh/config` so the first connection is reused:

```
Host *
    ControlMaster auto
    ControlPath ~/.ssh/cm-%r@%h:%p
    ControlPersist 10m
```

### Auto-yes on remote

Auto-yes is **off by default** on remote hosts — auto-approving agent
actions on a shared/production box is riskier than locally. Toggle it
per-instance with `a`; the TUI warns when auto-yes is on for a remote
instance.

### Attaching

`↵` / `o` attaches to the selected instance's tmux session. For a remote
instance this opens an interactive `ssh -t <host> tmux attach-session` under
a local PTY, so you interact with the remote agent directly. Detach with
`ctrl-q` as usual.

---

## Installation

```bash
curl -fsSL https://raw.githubusercontent.com/yro7/boulez/main/install.sh | bash
```

This puts the `boulez` binary in `~/.local/bin`.

To use a custom name for the binary:

```bash
curl -fsSL https://raw.githubusercontent.com/yro7/boulez/main/install.sh | bash -s -- --name <your-binary-name>
```

### Prerequisites

- [tmux](https://github.com/tmux/tmux/wiki/Installing)
- [gh](https://cli.github.com/)

### Build from source

```bash
go build -o boulez .
```

---

## Usage

```
Usage:
  boulez [flags]
  boulez [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  ctl         Send a JSON-RPC syscall to the kernel's control socket
  daemon      Manage the boulez daemon (the kernel / control authority)
  debug       Print debug information like config paths
  help        Help about any command
  repo-import Import git repos from your IDEs into the registry
  reset       Reset all stored instances
  version     Print the version number of boulez

Flags:
  -y, --autoyes          If enabled, all instances will automatically accept prompts for claude code & aider
  -h, --help             help for boulez
  -p, --program string   Program to run in new instances (e.g. 'aider --model ollama_chat/gemma3:1b')
```

Run the application with:

```bash
boulez
```

NOTE: The default program is `claude` and we recommend using the latest version.

<b>Using Boulez with other AI assistants:</b>
- For [Codex](https://github.com/openai/codex): Set your API key with `export OPENAI_API_KEY=<your_key>`
- Launch with specific assistants:
   - Codex: `boulez -p "codex"`
   - Aider: `boulez -p "aider ..."`
   - Gemini: `boulez -p "gemini"`
- Make this the default, by modifying the config file (locate with `boulez debug`)

---

## Menu

The menu at the bottom of the screen shows available commands.

##### Instance/Session Management
- `n` - Create a new session
- `N` - Create a new session with a prompt
- `D` - Kill (delete) the selected session
- `↑/j`, `↓/k` - Navigate between sessions

##### Actions
- `↵/o` - Attach to the selected session to reprompt
- `ctrl-q` - Detach from session
- `s` - Commit and push branch to github
- `c` - Checkout. Commits changes and pauses the session
- `r` - Resume a paused session
- `?` - Show help menu

##### Navigation
- `tab` - Switch between preview tab and diff tab
- `q` - Quit the application
- `shift-↓/↑` - scroll in diff view

---

## Configuration

Boulez stores its configuration in `~/.boulez/config.json`. You can find the
exact path by running `boulez debug`.

#### Profiles

Profiles let you define multiple named program configurations and switch
between them when creating a new session. When more than one profile is
defined, the session creation overlay shows a profile picker that you can
navigate with `←`/`→`.

To configure profiles, add a `profiles` array to your config file and set
`default_program` to the name of the profile to select by default:

```json
{
  "default_program": "claude",
  "profiles": [
    { "name": "claude", "program": "claude" },
    { "name": "codex", "program": "codex" },
    { "name": "aider", "program": "aider --model ollama_chat/gemma3:1b" }
  ]
}
```

Each profile has two fields:

| Field     | Description                                              |
|-----------|----------------------------------------------------------|
| `name`    | Display name shown in the profile picker                 |
| `program` | Shell command used to launch the agent for that profile  |

If no profiles are defined, boulez uses `default_program` directly as the
launch command (the default is `claude`).

---

## FAQs

#### Failed to start new session

If you get an error like `failed to start new session: timed out waiting for
tmux session`, update the underlying program (ex. `claude`) to the latest
version.

---

## How It Works

1. **tmux** creates isolated terminal sessions for each agent.
2. **git worktrees** isolate codebases so each session works on its own branch.
3. A **kernel + daemon** owns the control authority; the TUI and `boulez ctl`
   are its clients.
4. A **`program.Adapter` seam** makes agent support modular (one file per
   agent under `program/`).
5. An **SSH host abstraction** lets an instance run on a remote machine while
   being supervised locally.

---

## License

[AGPL-3.0](LICENSE.md)
