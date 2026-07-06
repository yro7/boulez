package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yro7/boulez/kernel"
)

// TestCtl_Spawn_BuildsCorrectRequest verifies `boulez ctl spawn_worker`
// constructs the correct wire request — including the host binding when
// --host is passed — WITHOUT going to the daemon, via the captureHook.
func TestCtl_Spawn_BuildsCorrectRequest(t *testing.T) {
	var captured []kernel.Request
	prev := captureHook
	captureHook = func(reqs []kernel.Request) error {
		captured = reqs
		return nil
	}
	t.Cleanup(func() { captureHook = prev })

	cmd := NewCtlSpawnCmd()
	cmd.SetArgs([]string{
		"--repo", "/root/proj",
		"--host", "dev-machine",
		"--program", "pi",
		"--prompt", "test",
	})
	require.NoError(t, cmd.Execute())

	require.Len(t, captured, 1)
	assert.Equal(t, "spawn_worker", captured[0].Method)

	var p kernel.SpawnParams
	require.NoError(t, json.Unmarshal(captured[0].Params, &p))
	assert.Equal(t, "/root/proj", p.Repo)
	assert.Equal(t, "dev-machine", p.Host, "--host must reach the wire as the host binding")
	assert.Equal(t, "pi", p.Program)
	assert.Equal(t, "test", p.Prompt)
}

// TestCtl_Spawn_OmitsHostWhenFlagAbsent verifies that omitting --host leaves
// the wire host empty, so the daemon defaults to Local (the documented
// behaviour: omitting --host keeps the local default).
func TestCtl_Spawn_OmitsHostWhenFlagAbsent(t *testing.T) {
	var captured []kernel.Request
	prev := captureHook
	captureHook = func(reqs []kernel.Request) error {
		captured = reqs
		return nil
	}
	t.Cleanup(func() { captureHook = prev })

	cmd := NewCtlSpawnCmd()
	cmd.SetArgs([]string{"--repo", "/repo", "--program", "pi"})
	require.NoError(t, cmd.Execute())

	require.Len(t, captured, 1)
	var p kernel.SpawnParams
	require.NoError(t, json.Unmarshal(captured[0].Params, &p))
	assert.Equal(t, "", p.Host)
}

// TestCtl_AsSpawnWorker_PropagatesHost proves `boulez ctl as <id> spawn_worker
// --host <alias>` carries the host through the authenticated session: the
// captured request sequence is [authenticate, spawn_worker] and the
// spawn_worker params carry the host alias. This is the parity guarantee for
// the orchestrator/CLI path with the TUI host selector.
func TestCtl_AsSpawnWorker_PropagatesHost(t *testing.T) {
	var captured []kernel.Request
	prev := captureHook
	captureHook = func(reqs []kernel.Request) error {
		captured = reqs
		return nil
	}
	t.Cleanup(func() { captureHook = prev })

	root := NewCtlCmd()
	// `as` disables flag parsing on itself and forwards the rest to the
	// wrapped syscall; runCtlAs builds the subcommand via buildCtlSub.
	root.SetArgs([]string{"as", "46f7194a-c9d0-46c8-9f84-8f31e9caf134",
		"spawn_worker", "--repo", "/root/proj", "--host", "dev-machine",
		"--program", "pi", "--prompt", "test"})
	require.NoError(t, root.Execute())

	require.Len(t, captured, 2, "as must send authenticate then the syscall")
	assert.Equal(t, "authenticate", captured[0].Method)

	assert.Equal(t, "spawn_worker", captured[1].Method)
	var p kernel.SpawnParams
	require.NoError(t, json.Unmarshal(captured[1].Params, &p))
	assert.Equal(t, "/root/proj", p.Repo)
	assert.Equal(t, "dev-machine", p.Host, "as-spawn must propagate --host to the wire")
}
