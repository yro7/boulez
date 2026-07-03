# cs2 — a fork of Claude Squad

> ⚠️ **This is a fork.** cs2 is a local fork of [claude-squad](https://github.com/smtg-ai/claude-squad) (upstream commit `5a604f7`, v1.0.19), **not** the upstream project. It is not affiliated with `smtg-ai`. The badges, links, and screenshots below originate from upstream and are kept for reference only.
>
> Before working here, read **[AGENTS.md](./AGENTS.md)** for the project's goals, non-negotiable rules, and code philosophy.
>
> Differences from upstream:
> - Ships as a separate `cs2` binary (the official Homebrew `cs` is left untouched).
> - **Modular agent support** via a `program.Adapter` seam: adding a new agent (Pi, Codex, Amp, …) is one file under `program/` + one `Register` line, with no edits to the tmux core, TUI, or daemon.
> - **Multi-repo orchestration** (in progress): the TUI centralizes instances running across several different repositories.
> - A small Pi ↔ cs2 ready-signal bridge (see `extensions/pi-cs2.ts` + `program/pi.go`).
>
> Upstream documentation below is preserved as-is for feature reference.

## cs2: repo registry & one-shot IDE import

cs2 keeps a registry of known repositories (`~/.cs2/repos.json`) used to
pre-populate the repo selector when creating an instance. Repos are added to
the registry automatically as you use them; you can also **import them in a
one-shot, manual pass** from the IDEs you already use.

```bash
# Preview (read-only) what would be imported, without writing:
cs2 repo-import --dry-run

# Import: scan all installed VS Code-family IDEs, keep only git repos,
# add the new ones to the registry:
cs2 repo-import

# Restrict the scan to a single IDE:
cs2 repo-import --ide cursor
```

Supported IDEs (all VS Code-family forks sharing the same `storage.json`
layout): `vscode`, `cursor`, `windsurf`, `antigravity`, `vscodium`, `pearai`,
`void`, `trae`.

This is a **one-shot, manual** import — cs2 never reads IDE state
automatically, so a format change in an IDE's `storage.json` never affects
normal operation. The IDE parsing is isolated in the `ideimport/` package.

---

## Remote instances (SSH)

cs2 can run an instance's whole environment (git worktree, tmux session,
agent) on a **remote machine** over SSH while you supervise it from the local
TUI. A single dashboard can then span several machines — e.g.
`(A, local)`, `(A, gpu-box)`, `(B, gpu-box)`.

### How it works

Every command, filesystem operation, and PTY the instance needs is routed
through the system `ssh` binary, reusing your existing SSH config
(`~/.ssh/config`, agent, keys). cs2 never stores credentials. An instance on
host `dev-machine` runs `ssh dev-machine git ...`, `ssh dev-machine tmux ...`,
and attaches via `ssh -t dev-machine tmux attach-session -t <name>`.

### Picking a host

When creating an instance (`n` / `N`), the first screen is the **host
selector**. `local` (this machine) is always listed first; any SSH aliases
you have used before follow; you can also type a new alias as free text — it
is remembered for next time (stored in `~/.cs2/hosts.json`).

The alias must resolve through your SSH config / known hosts. cs2 treats it as
opaque — user, port, and key resolution are ssh's job.

### Preconditions on the remote host

The remote machine must have installed:

- **tmux** (cs2 drives a remote tmux session), and
- **the agent binary** you launch (e.g. `claude`, `codex`, `aider`, …),
  reachable on the remote `PATH`.

cs2 creates the worktree under `~/.cs2/worktrees` on the remote host; the `~`
is expanded by the remote shell, so it lands in the remote user's home.

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

Managing the control master from within cs2 is a roadmap item (not in v2).

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

### Port-forwarding

If the remote agent starts a dev server, forward the port yourself
(e.g. `ssh -L 3000:localhost:3000 dev-machine`). cs2 does not auto-forward
ports in v2.

---

# Claude Squad (upstream reference) [![CI](https://github.com/smtg-ai/claude-squad/actions/workflows/build.yml/badge.svg)](https://github.com/smtg-ai/claude-squad/actions/workflows/build.yml) [![GitHub Release](https://img.shields.io/github/v/release/smtg-ai/claude-squad)](https://github.com/smtg-ai/claude-squad/releases/latest)

[Claude Squad](https://smtg-ai.github.io/claude-squad/) is a terminal app that manages multiple [Claude Code](https://github.com/anthropics/claude-code), [Codex](https://github.com/openai/codex), [Gemini](https://github.com/google-gemini/gemini-cli) (and other local agents including [Aider](https://github.com/Aider-AI/aider)) in separate workspaces, allowing you to work on multiple tasks simultaneously.


![Claude Squad Screenshot](assets/screenshot.png)

### Highlights
- Complete tasks in the background (including yolo / auto-accept mode!)
- Manage instances and tasks in one terminal window
- Review changes before applying them, checkout changes before pushing them
- Each task gets its own isolated git workspace, so no conflicts

<br />

https://github.com/user-attachments/assets/aef18253-e58f-4525-9032-f5a3d66c975a

<br />

### Installation

Both Homebrew and manual installation will install Claude Squad as `cs` on your system.

#### Homebrew

```bash
brew install claude-squad
ln -s "$(brew --prefix)/bin/claude-squad" "$(brew --prefix)/bin/cs"
```

#### Manual

Claude Squad can also be installed by running the following command:

```bash
curl -fsSL https://raw.githubusercontent.com/smtg-ai/claude-squad/main/install.sh | bash
```

This puts the `cs` binary in `~/.local/bin`.

To use a custom name for the binary:

```bash
curl -fsSL https://raw.githubusercontent.com/smtg-ai/claude-squad/main/install.sh | bash -s -- --name <your-binary-name>
```

### Prerequisites

- [tmux](https://github.com/tmux/tmux/wiki/Installing)
- [gh](https://cli.github.com/)

### Usage

```
Usage:
  cs [flags]
  cs [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  debug       Print debug information like config paths
  help        Help about any command
  reset       Reset all stored instances
  version     Print the version number of claude-squad

Flags:
  -y, --autoyes          [experimental] If enabled, all instances will automatically accept prompts for claude code & aider
  -h, --help             help for claude-squad
  -p, --program string   Program to run in new instances (e.g. 'aider --model ollama_chat/gemma3:1b')
```

Run the application with:

```bash
cs
```
NOTE: The default program is `claude` and we recommend using the latest version.

<br />

<b>Using Claude Squad with other AI assistants:</b>
- For [Codex](https://github.com/openai/codex): Set your API key with `export OPENAI_API_KEY=<your_key>`
- Launch with specific assistants:
   - Codex: `cs -p "codex"`
   - Aider: `cs -p "aider ..."`
   - Gemini: `cs -p "gemini"`
- Make this the default, by modifying the config file (locate with `cs debug`)

<br />

#### Menu
The menu at the bottom of the screen shows available commands: 

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

### Configuration

Claude Squad stores its configuration in `~/.claude-squad/config.json`. You can find the exact path by running `cs debug`.

#### Profiles

Profiles let you define multiple named program configurations and switch between them when creating a new session. When more than one profile is defined, the session creation overlay shows a profile picker that you can navigate with `←`/`→`.

To configure profiles, add a `profiles` array to your config file and set `default_program` to the name of the profile to select by default:

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

If no profiles are defined, Claude Squad uses `default_program` directly as the launch command (the default is `claude`).

### FAQs

#### Failed to start new session

If you get an error like `failed to start new session: timed out waiting for tmux session`, update the
underlying program (ex. `claude`) to the latest version.

### How It Works

1. **tmux** to create isolated terminal sessions for each agent
2. **git worktrees** to isolate codebases so each session works on its own branch
3. A simple TUI interface for easy navigation and management

### License

[AGPL-3.0](LICENSE.md)

### Star History

[![Star History Chart](https://api.star-history.com/svg?repos=smtg-ai/claude-squad&type=Date)](https://www.star-history.com/#smtg-ai/claude-squad&Date)
