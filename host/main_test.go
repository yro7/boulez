package host

import (
	"os"
	"testing"

	"github.com/yro7/boulez/log"
)

// TestMain initializes the logger before any host tests run. Several host code
// paths log warnings on best-effort failure (e.g. SSHHost.EnsureConnected when
// the ControlMaster fails to start); without an initialized logger those would
// nil-deref under test. Mirrors the pattern in config/app/ui test mains.
func TestMain(m *testing.M) {
	log.Initialize(false)
	defer log.Close()
	os.Exit(m.Run())
}
