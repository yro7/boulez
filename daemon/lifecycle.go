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
