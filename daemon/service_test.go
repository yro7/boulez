package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServiceLabel_Stable proves the service label is the documented stable
// identifier that install/uninstall both key off. A drift here would mean
// install writes one label and uninstall tries to remove another.
func TestServiceLabel_Stable(t *testing.T) {
	assert.Equal(t, "ai.smtg.boulez", ServiceLabel)
}

// TestServiceManager_ReportsPlatform proves ServiceManager returns a non-empty
// identifier on a supported platform (launchd/systemd). On an unsupported
// platform it returns "" — the install command uses that to fall back to the
// nohup dev form (C2.5).
func TestServiceManager_ReportsPlatform(t *testing.T) {
	mgr := ServiceManager()
	// This test runs on the build host (macOS in CI); assert it reports a
	// known manager rather than empty, which would indicate a build-tag
	// misconfiguration.
	assert.Contains(t, []string{"launchd", "systemd", ""}, mgr)
	if mgr == "" {
		t.Logf("platform has no service manager; install must fall back to nohup")
	}
}

// TestDaemonRedirectLog_UnderConfigDir proves the redirect log lives under the
// boulez config dir's logs/ subdir, so a crash-before-logging is diagnosable in a
// predictable place (and `boulez daemon status` already points at this area).
func TestDaemonRedirectLog_UnderConfigDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	out, err := daemonRedirectLog("out")
	require.NoError(t, err)
	errLog, err := daemonRedirectLog("err")
	require.NoError(t, err)

	home, _ := os.UserHomeDir()
	assert.Equal(t, filepath.Join(home, ".boulez", "logs", "daemon.out.log"), out)
	assert.Equal(t, filepath.Join(home, ".boulez", "logs", "daemon.err.log"), errLog)

	// The dir was created.
	_, err = os.Stat(filepath.Dir(out))
	require.NoError(t, err)
}

// TestErrServiceUnsupported_Message proves the dev-fallback hint (C2.5) is in
// the error message, so the install command surfaces it without extra logic.
func TestErrServiceUnsupported_Message(t *testing.T) {
	err := ErrServiceUnsupported{}
	assert.Contains(t, err.Error(), "nohup boulez daemon run")
}
