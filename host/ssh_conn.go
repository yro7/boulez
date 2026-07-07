package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yro7/boulez/config"
	"github.com/yro7/boulez/log"
)

// ControlMaster muxing for SSHHost.
//
// Problem this solves: boulez drives a remote host (one ssh alias) with a
// high rate of one-shot `ssh <alias> <cmd>` invocations — a spawn is a burst of
// git/tmux commands, and the daemon poll loop fires `tmux capture-pane` +
// `git diff --numstat` for every live remote instance every second. Each
// one-shot `ssh` is a fresh TCP+auth round-trip, and crucially re-attempts
// every LocalForward/RemoteForward in the alias's ~/.ssh/config — so a prior
// connection holding those ports makes every subsequent connection emit
// `bind 127.0.0.1:PORT: Address already in use` and races connection-setup
// latency against short tmux poll timeouts, causing intermittent
// `timed out waiting for tmux session ...: <nil>`.
//
// Fix: one long-lived SSH ControlMaster per alias. Once a master is up, slave
// commands that carry `-o ControlPath=<socket>` multiplex over it — no new
// TCP handshake, no new auth, no new port binding (the master holds the
// forwards; slaves inherit them transparently). Auth happens once per host
// lifetime, cutting overhead ~10–50×.
//
// Seam: the master is started by sshMaster.Ensure (called once per instance
// Start via SSHHost.EnsureConnected). Slave command builders
// (sshExecutor/sshFS/sshPtyFactory) add `-o ControlPath=<socket>` to their
// argv. With ControlMaster left at its default (no), a slave only USES an
// existing master — it never creates one — so a dead/missing socket falls
// back gracefully to a direct connection (non-breaking). This was verified
// empirically: ControlMaster=no + ControlPath=<live socket> multiplexes;
// ControlMaster=no + ControlPath=<dead/missing socket> opens a normal
// connection; ControlMaster=no + ControlPath to a nonexistent parent dir
// also falls back (no socket-creation attempt, so no failure).

// sshRunner runs `ssh <args...>` and returns trimmed combined output + err.
// It is the seam that makes the master lifecycle testable without a real host:
// tests inject a fake that records the `-O check`/`-M`/`-O exit` argv and
// scripts the desired state.
type sshRunner interface {
	Run(args ...string) (string, error)
}

// realSSHRunner is the production sshRunner: shells out to the system ssh.
type realSSHRunner struct{}

func (realSSHRunner) Run(args ...string) (string, error) {
	out, err := exec.Command(sshBin, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// sshControlDir is the directory holding the per-alias control sockets:
// <boulez-config-dir>/ssh (= ~/.boulez/ssh). Swappable (package var) so tests
// can point it at a temp dir without touching the real config dir, mirroring
// the remoteHome stub pattern.
var sshControlDir = func() (string, error) {
	d, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "ssh"), nil
}

// socketForAlias returns the deterministic ControlPath socket for an alias:
// <sshControlDir>/<sanitized-alias>.sock. The alias is sanitized (path-unsafe
// chars → _) so a weird alias cannot escape the dir or fail to bind. Returns
// "" + error if the control dir can't be resolved (callers skip muxing then).
func socketForAlias(alias string) (string, error) {
	dir, err := sshControlDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sanitizeAlias(alias)+".sock"), nil
}

// sanitizeAlias replaces path-unsafe characters in an ssh alias so it is a
// safe single path component. Aliases are normally alphanumeric+dash, but a
// user could configure "user@host:2222" which would otherwise break the
// socket path. Pure so it's unit-testable.
func sanitizeAlias(alias string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return r.Replace(alias)
}

// sshMaster ensures a long-lived SSH ControlMaster is running for an alias, so
// every subsequent `ssh <alias> ...` carrying `-o ControlPath=<socket>`
// multiplexes over one connection. One master per alias; lazily started;
// registered for daemon-shutdown cleanup. Idempotent via `ssh -O check`.
type sshMaster struct {
	alias  string
	socket string // "" => muxing unavailable (config dir unresolvable); skip
	runner  sshRunner
}

// newSSHMaster builds an sshMaster for an alias. The socket path is computed
// eagerly (it does not require the master to be running); best-effort: a
// resolution failure leaves socket "" and muxing is silently skipped.
func newSSHMaster(alias string) sshMaster {
	socket, err := socketForAlias(alias)
	if err != nil {
		log.WarningLog.Printf("ssh master: disabling muxing for %s (no control dir): %v", alias, err)
		return sshMaster{alias: alias, runner: realSSHRunner{}}
	}
	return sshMaster{alias: alias, socket: socket, runner: realSSHRunner{}}
}

// withRunner is the test seam: inject a fake sshRunner to unit-test the
// Ensure/Check/Stop state machine without a real host.
func (m sshMaster) withRunner(r sshRunner) sshMaster {
	m.runner = r
	return m
}

// socketForSlave returns the ControlPath value slave commands should carry to
// reuse this master. Empty when muxing is unavailable (slaves then emit plain
// one-shot `ssh <alias> ...`). Exposed so SSHHost can thread it into its
// executor/FS/PTY factories.
func (m sshMaster) socketForSlave() string { return m.socket }

// Ensure starts the master if none is running. Idempotent: Check first, and
// only start if down. Creates the socket dir (ControlPath requires it for -M
// to bind). Honors the alias's own ~/.ssh/config (LocalForwards, keys) — the
// master is just `ssh <alias> ...`. Safe against a stale socket from a
// crashed prior master: Check returns false and Ensure re-runs -M, which
// rebinds the socket. Serialized per alias so concurrent SSHHosts (e.g. a
// spawn burst) don't race the check-then-start.
func (m sshMaster) Ensure() error {
	if m.socket == "" {
		return nil // muxing unavailable; slaves fall back to one-shot
	}
	mastersMu.Lock()
	defer mastersMu.Unlock()
	if m.isUpLocked() {
		// Adopt an already-running master (e.g. left over from a previous
		// daemon that crashed before StopAllMasters). We didn't start it, but
		// we're now its owner: registering it ensures StopAllMasters can tear
		// it down on graceful shutdown instead of leaking it.
		registeredMasters[m.alias] = m
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.socket), 0o700); err != nil {
		return fmt.Errorf("ssh master mkdir %s: %w", filepath.Dir(m.socket), err)
	}
	// Self-heal a stale socket file. A master whose underlying connection died
	// (e.g. a ProxyJump/network blip) can linger under ControlPersist: its
	// control channel refuses connections (isUpLocked already reported it down)
	// but its socket file remains on disk. A fresh `ssh -M -S <socket>` would
	// then fail to bind ("Address already in use") because the path exists.
	// Removing it is safe here: we hold mastersMu and the master is confirmed
	// not serving. Best-effort — a remove failure is logged, not fatal.
	if err := os.Remove(m.socket); err != nil && !os.IsNotExist(err) {
		log.WarningLog.Printf("ssh master: could not remove stale socket %s: %v", m.socket, err)
	}
	// -fN backgrounds after auth (no remote command); -M master; -S socket;
	// ControlPersist=yes keeps it alive after the establishing process exits.
	// sshHardenArgs (BatchMode + ConnectTimeout) bounds auth so an unreachable
	// or prompting host fails fast instead of hanging (no TTY to prompt on).
	args := append([]string{"-fN", "-M", "-S", m.socket, "-o", "ControlPersist=yes"}, sshHardenArgs()...)
	args = append(args, m.alias)
	_, err := m.runner.Run(args...)
	if err != nil {
		return fmt.Errorf("ssh master ensure %s: %w", m.alias, err)
	}
	registeredMasters[m.alias] = m
	return nil
}

// Check reports whether a live master is running for this alias's socket.
func (m sshMaster) Check() bool {
	if m.socket == "" {
		return false
	}
	mastersMu.Lock()
	defer mastersMu.Unlock()
	return m.isUpLocked()
}

// isUpLocked is Check without locking (caller holds mastersMu).
func (m sshMaster) isUpLocked() bool {
	_, err := m.runner.Run("-O", "check", "-S", m.socket, m.alias)
	return err == nil
}

// Stop tears down the master via `ssh -O exit`. No-op if no master. Best-effort:
// a failure (e.g. already gone) is returned but does not block shutdown.
func (m sshMaster) Stop() error {
	if m.socket == "" {
		return nil
	}
	_, err := m.runner.Run("-O", "exit", "-S", m.socket, m.alias)
	return err
}

// registeredMasters tracks live masters by alias so the daemon can stop them
// all on shutdown. Keyed by alias (one master per alias, shared across
// SSHHosts). Guarded by mastersMu.
var (
	mastersMu         sync.Mutex
	registeredMasters = map[string]sshMaster{}
)

// StopAllMasters tears down every live ControlMaster registered by Ensure.
// Called from the daemon shutdown path so masters don't outlive the daemon.
// Each Stop is best-effort; one failure doesn't block the rest.
func StopAllMasters() {
	mastersMu.Lock()
	ms := registeredMasters
	registeredMasters = map[string]sshMaster{}
	mastersMu.Unlock()
	for _, m := range ms {
		if err := m.Stop(); err != nil {
			log.WarningLog.Printf("ssh master stop %s: %v", m.alias, err)
		}
	}
}

// sshControlArgs returns the ssh argv options that make a slave command
// multiplex over a ControlMaster socket (if one is up). Empty when muxing is
// unavailable (slaves then run as plain one-shot `ssh <alias> ...`).
// ControlMaster is intentionally left at its default (no): a slave only USES
// an existing master, never creates one — so a dead/missing socket falls back
// to a direct connection instead of failing (non-breaking).
func sshControlArgs(socket string) []string {
	if socket == "" {
		return nil
	}
	return []string{"-o", "ControlPath=" + socket}
}

// sshConnectTimeoutSecs bounds every ssh connect the daemon makes. Kept short:
// a remote-host probe (has-session, git, $HOME) must fail fast so it never
// stalls the daemon boot or a poll tick.
const sshConnectTimeoutSecs = 10

// sshHardenArgs returns the ssh options that make boulez's non-interactive ssh
// traffic well-behaved: fail fast instead of hanging, and never fight the user
// over their forward ports.
//
//   - BatchMode=yes disables ALL interactive prompts (password, passphrase,
//     host-key confirmation). The daemon has no TTY to answer them, so a prompt
//     would block indefinitely; BatchMode turns it into an immediate error.
//     (Fixes the daemon-wedge bug: a remote instance's liveness probe ran during
//     daemon boot and, with no BatchMode/ConnectTimeout, an unreachable or
//     prompting host hung that ssh — and thus the whole daemon — forever.)
//   - ConnectTimeout bounds the TCP+auth handshake so an unreachable host fails
//     in seconds instead of the OS default (minutes, effectively a hang).
//   - ClearAllForwardings=yes suppresses every LocalForward/RemoteForward/
//     DynamicForward the alias's ~/.ssh/config declares. boulez's automated ssh
//     is a pure command channel (tmux/git/fs) — it needs no forwards. Without
//     this, a master (or one-shot fallback) re-attempts the alias's forwards on
//     EVERY connection; when the user's own ssh session (or a lingering master)
//     already holds those ports, ssh emits `bind ...: Address already in use` /
//     `Could not request local forwarding` and races that setup against short
//     tmux poll timeouts — a primary cause of the remote-instance UI freeze.
//     Clearing them lets boulez's connection coexist peacefully with the user's,
//     and does not touch ProxyJump. The interactive PTY attach keeps forwards
//     (see below).
//
// Applied to every non-interactive slave (executor/FS/$HOME) and the master.
// NOT applied to the interactive PTY attach (sshPtyFactory), which is
// user-driven and legitimately wants a terminal (and may want the forwards).
func sshHardenArgs() []string {
	return []string{
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", sshConnectTimeoutSecs),
		"-o", "ClearAllForwardings=yes",
	}
}
