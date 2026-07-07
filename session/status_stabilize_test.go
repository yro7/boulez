package session

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yro7/boulez/program"
)

// stabilize tick drives a single Stabilize call for the given instance with
// the HasUpdated-shaped observation (updated, observed status, stableFor) and
// returns the resulting status. streaks is carried across ticks.
func stabilize(streaks map[string]int, id string, prev Status, updated bool, observed program.Status, stableFor time.Duration) Status {
	return Stabilize(streaks, id, observed, updated, stableFor, prev)
}

// TestStabilize_HysteresisHoldsReadyOnJitter proves a single "source changed"
// tick does NOT demote a Ready instance back to Running. The pane chrome (an
// agent's animated spinner, context-usage percentage, the cursor) keeps hashing
// differently even when the agent is idle, so without hysteresis an idle
// instance flickers Ready↔Running every tick. The hysteresis requires
// ReadyToWorkingTicks consecutive "really working" ticks before flipping
// Ready→Running; a jittery source (updated, not-updated, updated) never
// accumulates enough consecutive ticks to flip.
func TestStabilize_HysteresisHoldsReadyOnJitter(t *testing.T) {
	streaks := make(map[string]int)
	id := "w1"
	prev := Ready

	// Tick 1: source "changed" (chrome jitter), adapter says Working. Without
	// hysteresis this would flip to Running immediately.
	prev = stabilize(streaks, id, prev, true, program.StatusWorking, 0)
	assert.Equal(t, Ready, prev, "a single working tick must not demote Ready")

	// Tick 2: source stable (no chrome change this tick), adapter still Working.
	// A stable tick resets the working streak, so the next jitter tick starts
	// counting from 1 again.
	prev = stabilize(streaks, id, prev, false, program.StatusWorking, 100*time.Millisecond)
	assert.Equal(t, Ready, prev, "stable tick keeps Ready")
	assert.Equal(t, 0, streaks[id], "stable tick resets the working streak")

	// Tick 3: jitter again. Streak is 1, still below threshold.
	prev = stabilize(streaks, id, prev, true, program.StatusWorking, 0)
	assert.Equal(t, Ready, prev, "streak=1 must not demote Ready")
}

// TestStabilize_HysteresisFlipsAfterThreshold proves that sustained "really
// working" ticks (the agent genuinely resumed work) DO eventually flip
// Ready→Running once the streak exceeds the threshold. The hysteresis delays
// the flip, it doesn't prevent it.
func TestStabilize_HysteresisFlipsAfterThreshold(t *testing.T) {
	streaks := make(map[string]int)
	id := "w1"
	prev := Ready

	// Drive ReadyToWorkingTicks consecutive working ticks. The instance must
	// hold Ready until the threshold, then flip to Running.
	for i := 1; i <= ReadyToWorkingTicks; i++ {
		prev = stabilize(streaks, id, prev, true, program.StatusWorking, 0)
		if i < ReadyToWorkingTicks {
			assert.Equal(t, Ready, prev,
				"tick %d (< threshold %d) must hold Ready", i, ReadyToWorkingTicks)
		}
	}
	assert.Equal(t, Running, prev,
		"after %d consecutive working ticks the instance flips to Running", ReadyToWorkingTicks)
}

// TestStabilize_ReadySignalResetsStreak proves an authoritative Ready signal
// (adapter StatusReady, e.g. Pi's journal stopReason) resets the working
// streak, so the next working phase starts counting from 1. Without this, a
// Ready signal that flickers in/out would accumulate a streak across the
// "ready" gaps and eventually false-flip.
func TestStabilize_ReadySignalResetsStreak(t *testing.T) {
	streaks := make(map[string]int)
	id := "w1"
	prev := Ready

	// Accumulate 2 working ticks (below the threshold).
	for i := 0; i < 2; i++ {
		prev = stabilize(streaks, id, prev, true, program.StatusWorking, 0)
	}
	require.Equal(t, 2, streaks[id])
	require.Equal(t, Ready, prev)

	// Ready signal reappears: StatusReady resets the streak.
	prev = stabilize(streaks, id, prev, true, program.StatusReady, 0)
	assert.Equal(t, Ready, prev)
	assert.Equal(t, 0, streaks[id], "a Ready signal resets the working streak")

	// One working tick after the reset: streak is 1, still below threshold.
	prev = stabilize(streaks, id, prev, true, program.StatusWorking, 0)
	assert.Equal(t, Ready, prev, "streak reset means we start counting from 1 again")
}

// TestStabilize_RunningStaysRunningNoHysteresis proves the hysteresis only
// gates the Ready→Running transition. A Running instance stays Running on a
// single working tick (no need to accumulate a streak from Running).
func TestStabilize_RunningStaysRunningNoHysteresis(t *testing.T) {
	streaks := make(map[string]int)
	id := "w1"
	prev := Running

	prev = stabilize(streaks, id, prev, true, program.StatusWorking, 0)
	assert.Equal(t, Running, prev)
	assert.Equal(t, 0, streaks[id], "no streak accumulated from a Running instance")
}

// TestStabilize_StableFallbackStillFlipsToReady proves the agent-agnostic
// stability fallback (stableFor >= threshold → Ready) still works alongside
// the hysteresis: a long-stable source flips to Ready and resets the streak.
func TestStabilize_StableFallbackStillFlipsToReady(t *testing.T) {
	streaks := make(map[string]int)
	id := "w1"
	prev := Running

	prev = stabilize(streaks, id, prev, false, program.StatusUnknown, StableReadyThreshold+time.Second)
	assert.Equal(t, Ready, prev, "stable fallback flips to Ready")
	assert.Equal(t, 0, streaks[id], "stable fallback resets the streak")
}

// TestStabilize_PermissionLeavesStatusUnchanged proves a permission prompt
// does not change the status (the agent is waiting for a permission decision,
// not free input) and does not touch the streak.
func TestStabilize_PermissionLeavesStatusUnchanged(t *testing.T) {
	streaks := make(map[string]int)
	id := "w1"

	// From Running: stays Running.
	assert.Equal(t, Running, stabilize(streaks, id, Running, true, program.StatusPermission, 0))
	// From Ready: stays Ready.
	assert.Equal(t, Ready, stabilize(streaks, id, Ready, true, program.StatusPermission, 0))
	assert.Empty(t, streaks, "permission does not accumulate a streak")
}

// TestStabilize_UnknownHoldsReadyWhenStableShort proves the agent-agnostic
// hold-Ready path: a Ready instance with an unknown adapter (no authoritative
// signal) and a source stable for less than the threshold holds Ready and
// resets the streak.
func TestStabilize_UnknownHoldsReadyWhenStableShort(t *testing.T) {
	streaks := make(map[string]int)
	id := "w1"
	prev := Ready

	prev = stabilize(streaks, id, prev, false, program.StatusUnknown, 5*time.Second)
	assert.Equal(t, Ready, prev, "Ready holds when stable but not long enough")
	assert.Equal(t, 0, streaks[id], "a stable tick resets the streak")
}

// TestStabilize_UnknownRunningKeepsRunningWhenStableShort proves a Running
// instance with no change and not stable long enough keeps Running.
func TestStabilize_UnknownRunningKeepsRunningWhenStableShort(t *testing.T) {
	streaks := make(map[string]int)
	id := "w1"
	prev := Running

	prev = stabilize(streaks, id, prev, false, program.StatusUnknown, 5*time.Second)
	assert.Equal(t, Running, prev)
	assert.Equal(t, 0, streaks[id])
}
