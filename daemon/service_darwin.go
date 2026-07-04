//go:build darwin

package daemon

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"text/template"
)

// launchAgentPath is the macOS LaunchAgent plist location for the cs2 daemon.
// User-level (LaunchAgent, not LaunchDaemon) so it runs without root and
// survives reboot for the logged-in user (RunAtLoad + KeepAlive).
func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir for LaunchAgent path: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", ServiceLabel+".plist"), nil
}

// plistTpl is the launchd plist. RunAtLoad starts the daemon at login;
// KeepAlive restarts it if it exits (crash, OOM, reboot). ProgramArguments
// invokes `cs2 daemon run` (the canonical foreground entrypoint, D2/C1.1).
// Standard{Out,Error}Path capture the daemon's stdout/stderr so a crash is
// diagnosable without `cs2 daemon log` (which tails the claudesquad log).
const plistTpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{ .Label }}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{{ .Exe }}</string>
        <string>daemon</string>
        <string>run</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{{ .OutLog }}</string>
    <key>StandardErrorPath</key>
    <string>{{ .ErrLog }}</string>
</dict>
</plist>
`

// renderPlist writes the launchd plist into w. Extracted from install so the
// generated XML is testable without invoking launchctl. The plist pins the
// service label, the binary path, RunAtLoad+KeepAlive, and the redirect logs.
func renderPlist(w *bytes.Buffer, exe, outLog, errLog string) error {
	return template.Must(template.New("plist").Parse(plistTpl)).Execute(w, struct {
		Label  string
		Exe    string
		OutLog string
		ErrLog string
	}{
		Label:  ServiceLabel,
		Exe:    exe,
		OutLog: outLog,
		ErrLog: errLog,
	})
}

func uidImpl() (string, error) {
	return strconv.Itoa(os.Getuid()), nil
}

func serviceManager() string { return "launchd" }

func install() error {
	exe, err := exePath()
	if err != nil {
		return err
	}
	plistPath, err := launchAgentPath()
	if err != nil {
		return err
	}
	uid, err := uid()
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

	var buf bytes.Buffer
	if err := renderPlist(&buf, exe, outLog, errLog); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	if err := os.WriteFile(plistPath, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write plist %s: %w", plistPath, err)
	}

	// bootstrap loads the agent into the user's gui domain. If it is already
	// loaded (re-install), bootout first so the new plist takes effect. A
	// missing agent on bootout is a no-op.
	domain := fmt.Sprintf("gui/%s", uid)
	_ = exec.Command("launchctl", "bootout", domain+"/"+ServiceLabel).Run()

	boot := exec.Command("launchctl", "bootstrap", domain, plistPath)
	if out, err := boot.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, string(out))
	}
	return nil
}

func uninstall() error {
	uid, err := uid()
	if err != nil {
		return err
	}
	plistPath, err := launchAgentPath()
	if err != nil {
		return err
	}
	domain := fmt.Sprintf("gui/%s", uid)
	// bootout stops the agent and unloads it; a missing service is not an
	// error (idempotent uninstall).
	if out, err := exec.Command("launchctl", "bootout", domain+"/"+ServiceLabel).CombinedOutput(); err != nil {
		// "No service" is fine; surface anything else but still remove the
		// plist so install is clean next time.
		_ = string(out)
	}
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist %s: %w", plistPath, err)
	}
	return nil
}
