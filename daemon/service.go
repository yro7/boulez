package daemon

import (
	"claude-squad/config"
	"fmt"
	"os"
	"path/filepath"
)

// ServiceLabel is the launchd label / systemd unit name for the cs2 daemon
// service. It is the stable identifier across install/uninstall so the two
// always agree on what they manage.
const ServiceLabel = "ai.smtg.cs2"

// ErrServiceUnsupported is returned by Install/Uninstall on platforms without
// launchd or systemd. The dev fallback (C2.5) is `nohup cs2 daemon run &`,
// documented by the install command's caller.
type ErrServiceUnsupported struct {
	Manager string // "" on platforms with no service manager
}

func (e ErrServiceUnsupported) Error() string {
	return "cs2 daemon: no supported service manager (launchd/systemd) on this platform; use `nohup cs2 daemon run &` as a dev fallback"
}

// Install installs the daemon as an OS service (launchd on macOS, systemd user
// unit on Linux). The service runs `cs2 daemon run` and is kept alive by the
// service manager. Platform-specific implementations live in service_darwin.go
// and service_linux.go; service_other.go returns ErrServiceUnsupported.
func Install() error {
	return install()
}

// Uninstall stops and removes the OS service. A no-op (not an error) if the
// service is not installed, so uninstall is idempotent.
func Uninstall() error {
	return uninstall()
}

// ServiceManager reports the service manager available on this platform:
// "launchd" on macOS, "systemd" on Linux, "" otherwise. Used by the install
// command's output and by the dev-fallback hint (C2.5).
func ServiceManager() string {
	return serviceManager()
}

// exePath returns the absolute path to the running cs2 binary. The service
// unit invokes this directly so the service always runs the same binary the
// user installed from.
func exePath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve cs2 executable path: %w", err)
	}
	// Resolve symlinks so the plist/unit points at the real file (launchd is
	// happy with symlinks, but a resolved path survives a brew upgrade that
	// swaps the symlink target).
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved, nil
	}
	return p, nil
}

// uid returns the current user's numeric UID as a string, for the launchd
// domain `gui/<uid>` (and potentially for systemd user units on multi-seat
// systems). Platform-specific via uidImpl.
func uid() (string, error) {
	return uidImpl()
}

// daemonRedirectLog returns the path to a file that captures the daemon
// process's stdout/stderr when run under the service manager. The service
// unit redirects here so a crash-before-logging is still diagnosable. Kept
// under the cs2 config dir so it is co-located with daemon.pid/daemon.log
// and `cs2 daemon status` already points the user at that area.
func daemonRedirectLog(kind string) (string, error) {
	dir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", fmt.Errorf("create daemon log dir: %w", err)
	}
	return filepath.Join(logDir, "daemon."+kind+".log"), nil
}
