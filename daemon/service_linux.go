//go:build linux

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// systemdUnitPath is the user systemd unit location for the boulez daemon. A
// user unit (not a system one) runs without root and survives reboot for the
// logged-in user (enable + WantedBy=default.target).
func systemdUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir for systemd unit path: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user", "boulez.service"), nil
}

// unitTpl is the systemd user service. Type=simple + ExecStart=boulez daemon run
// (the canonical foreground entrypoint, D2/C1.1). Restart=on-failure restarts
// the daemon if it crashes; RestartSec paces the retries. WantedBy makes the
// unit start at login.
const unitTpl = `[Unit]
Description=boulez daemon (kernel / control authority)
After=network.target

[Service]
Type=simple
ExecStart=%s daemon run
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`

func serviceManager() string { return "systemd" }

func uidImpl() (string, error) {
	return strconv.Itoa(os.Getuid()), nil
}

// renderUnit builds the systemd user unit content. Extracted from install so
// the generated unit is testable without invoking systemctl. The unit pins
// ExecStart = `boulez daemon run`, Restart=on-failure, and the redirect logs.
func renderUnit(exe, outLog, errLog string) string {
	content := fmt.Sprintf(unitTpl, exe)
	// Mirror stdout/stderr to a file so a crash is diagnosable without journal.
	content += fmt.Sprintf("\nStandardOutput=append:%s\nStandardError=append:%s\n", outLog, errLog)
	return content
}

func install() error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	outLog, err := daemonRedirectLog("out")
	if err != nil {
		return err
	}
	errLog, err := daemonRedirectLog("err")
	if err != nil {
		return err
	}

	content := renderUnit(exe, outLog, errLog)

	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return fmt.Errorf("create systemd user dir: %w", err)
	}
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write unit %s: %w", unitPath, err)
	}

	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w: %s", err, string(out))
	}
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", "boulez").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable --now: %w: %s", err, string(out))
	}
	return nil
}

func uninstall() error {
	unitPath, err := systemdUnitPath()
	if err != nil {
		return err
	}
	// disable --now stops and disables the unit; a missing unit is not an
	// error (idempotent uninstall). Ignore the error so the file removal
	// still happens.
	_, _ = exec.Command("systemctl", "--user", "disable", "--now", "boulez").CombinedOutput()

	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit %s: %w", unitPath, err)
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w: %s", err, string(out))
	}
	return nil
}
