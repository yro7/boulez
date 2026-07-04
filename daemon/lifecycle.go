package daemon

import (
	"claude-squad/config"
	"claude-squad/kernel"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ProbeSocket dials the kernel control socket to confirm a live daemon is
// actually serving. A socket file left behind by a crashed daemon fails the
// dial, so this is a true liveness check — not a mere file-existence probe.
// Returns nil if the daemon is reachable.
func ProbeSocket() error {
	socketPath, err := kernel.SocketPath()
	if err != nil {
		return fmt.Errorf("resolve socket path: %w", err)
	}
	return probeSocket(socketPath)
}

func probeSocket(socketPath string) error {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("dial kernel socket %s: %w (is the daemon running?)", socketPath, err)
	}
	_ = conn.Close()
	return nil
}

// EnsureRunning guarantees a live daemon is reachable on the control socket.
// It is the TUI's boot contract (decision D2): the TUI is a viewer of the
// kernel, and there is no degraded mode over a broken daemon.
//
// If the socket is already serving, EnsureRunning returns nil without
// launching anything (the daemon is canonical and long-lived). Otherwise it
// launches the daemon detached (LaunchDaemon is a no-op if another launcher
// is mid-flight, thanks to the O_EXCL launch lock) and actively waits for the
// socket to come up. On timeout it returns an error; the caller (the TUI)
// must NOT proceed — it should surface the daemon log tail and exit non-zero.
func EnsureRunning(timeout time.Duration) error {
	socketPath, err := kernel.SocketPath()
	if err != nil {
		return fmt.Errorf("resolve socket path: %w", err)
	}

	// Already up? Nothing to do — the daemon is canonical, do not relaunch.
	if err := probeSocket(socketPath); err == nil {
		return nil
	}

	if err := LaunchDaemon(); err != nil {
		return fmt.Errorf("launch daemon: %w", err)
	}

	// WaitForSocket polls for the socket file to appear. It is best-effort on
	// its own (a stat-able socket is not necessarily a serving one), so we
	// follow it with a real dial probe to catch a daemon that bound then died.
	if err := WaitForSocket(socketPath, timeout); err != nil {
		return fmt.Errorf("daemon socket did not come up within %s: %w", timeout, err)
	}
	if err := probeSocket(socketPath); err != nil {
		return fmt.Errorf("daemon socket appeared but is not serving: %w", err)
	}
	return nil
}

// PIDFile returns the path to the daemon's PID file under the cs2 config dir.
func PIDFile() (string, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return "", fmt.Errorf("failed to get config directory: %w", err)
	}
	return filepath.Join(dir, "daemon.pid"), nil
}

// ReadPID reads the daemon PID file. It returns the parsed PID and whether
// the file existed. A missing file is not an error: it means no daemon was
// ever recorded (e.g. fresh install, or the daemon was stopped cleanly).
func ReadPID() (pid int, exists bool, err error) {
	path, err := PIDFile()
	if err != nil {
		return 0, false, err
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read PID file: %w", rerr)
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(string(data)))
	if perr != nil {
		return 0, true, fmt.Errorf("invalid PID file format: %w", perr)
	}
	return pid, true, nil
}
