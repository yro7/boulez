package session

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInstance_LandedLifecycle proves the TUI-only landed hint can be set and
// read back, and that it defaults to false. This is the seam the TUI uses to
// mark an instance as landed (dimmed row + checkmark) after a successful land.
func TestInstance_LandedLifecycle(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{
		Title: "feat-x", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	assert.False(t, inst.Landed(), "landed defaults to false")

	inst.SetLanded(true)
	assert.True(t, inst.Landed())

	inst.SetLanded(false)
	assert.False(t, inst.Landed())
}

// TestInstance_LandingLifecycle proves the in-flight hint (used by the renderer
// to show a land spinner) defaults to false and toggles.
func TestInstance_LandingLifecycle(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{
		Title: "feat-x", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	assert.False(t, inst.Landing())

	inst.SetLanding(true)
	assert.True(t, inst.Landing())

	inst.SetLanding(false)
	assert.False(t, inst.Landing())
}

// TestInstance_LandedNotPersisted proves the landed/landing hints are TUI-only
// view state: they do NOT appear in InstanceData (ToInstanceData), so a save/load
// round-trip (or a wire snapshot) does not carry them. The hint is set by the
// TUI after a land and survives only because reconcileFleet reuses handles by
// ID without resetting these fields.
func TestInstance_LandedNotPersisted(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{
		Title: "feat-x", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	inst.SetLanded(true)
	inst.SetLanding(true)

	data := inst.ToInstanceData()
	// No field for landed/landing exists on InstanceData; the only bool is
	// AutoYes. Reconstruct from data and confirm the hints reset.
	roundTripped, err := FromInstanceData(data)
	require.NoError(t, err)
	assert.False(t, roundTripped.Landed(), "landed must not survive a save/load round-trip")
	assert.False(t, roundTripped.Landing(), "landing must not survive a save/load round-trip")
}
