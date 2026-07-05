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

// TestInstance_LandedPersisted proves the landed hint is now PERSISTED in
// InstanceData (ToInstanceData / FromInstanceData): a save/load round-trip
// carries it. This is the kernel-side persisted state the Merge/Land syscalls
// set after a successful merge so `boulez ctl list_instances` / `get_instance`
// reflect that an instance's branch has been merged into the target trunk.
// (Previously landed was TUI-only and did not survive a round-trip.)
func TestInstance_LandedPersisted(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{
		Title: "feat-x", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	inst.SetLanded(true)

	data := inst.ToInstanceData()
	assert.True(t, data.Landed, "landed must be serialized into InstanceData")

	roundTripped, err := FromInstanceData(data)
	require.NoError(t, err)
	assert.True(t, roundTripped.Landed(), "landed must survive a save/load round-trip")
}

// TestInstance_LandingNotPersisted proves the in-flight `landing` hint is
// TUI-only view state: it is NOT in InstanceData, so a save/load round-trip
// does not carry it. Unlike `landed` (now persisted + propagated by the
// kernel), `landing` is a transient spinner hint the TUI clears on
// landDoneMsg and has no meaning outside the TUI.
func TestInstance_LandingNotPersisted(t *testing.T) {
	inst, err := NewInstance(InstanceOptions{
		Title: "feat-x", Path: t.TempDir(), Program: "claude",
	})
	require.NoError(t, err)
	inst.SetLanding(true)

	data := inst.ToInstanceData()
	roundTripped, err := FromInstanceData(data)
	require.NoError(t, err)
	assert.False(t, roundTripped.Landing(), "landing must not survive a save/load round-trip")
}
