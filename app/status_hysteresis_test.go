package app

import (
	"context"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yro7/boulez/config"
	"github.com/yro7/boulez/program"
	"github.com/yro7/boulez/session"
	"github.com/yro7/boulez/ui"
)

// newStatusHome builds a minimal home with one started instance in the list,
// for driving metadataUpdateDoneMsg ticks at the status-reconciliation loop.
// It avoids the daemon/tmux/socket entirely: the instance is marked started
// for test purposes and its status is set directly.
func newStatusHome(t *testing.T, inst *session.Instance) *home {
	t.Helper()
	sp := spinner.New()
	h := &home{
		ctx:       context.Background(),
		appConfig: config.DefaultConfig(),
		state:     stateDefault,
	}
	h.list = ui.NewList(&sp, false)
	h.list.SetInstances([]*session.Instance{inst})
	h.workingStreak = make(map[string]int)
	return h
}

func mustStartedInstance(t *testing.T, id, title string, status session.Status) *session.Instance {
	t.Helper()
	inst, err := session.NewInstance(session.InstanceOptions{
		ID:    id,
		Title: title,
		Path:  t.TempDir(),
		Kind:  session.KindWorker,
	})
	require.NoError(t, err)
	inst.SetStatus(status)
	inst.MarkStartedForTest()
	return inst
}

// tickMetadata delivers a metadataUpdateDoneMsg for the given instance with
// the given HasUpdated-shaped result (updated, detected status, stableFor).
func tickMetadata(inst *session.Instance, updated bool, status program.Status, stableFor time.Duration) tea.Msg {
	return metadataUpdateDoneMsg{
		results: []instanceMetaResult{{
			instance:  inst,
			updated:   updated,
			status:    status,
			stableFor: stableFor,
		}},
	}
}

// TestStatusReconcile_HysteresisHoldsReadyOnJitter proves a single "pane
// changed" tick does NOT demote a Ready instance back to Running. The pane
// chrome (Pi's animated spinner, the context-usage percentage, the cursor)
// keeps hashing differently even when the agent is idle, so without hysteresis
// an idle instance flickers Ready↔Running every 500ms tick. The hysteresis
// requires `readyToWorkingTicks` consecutive "really working" ticks before
// flipping Ready→Running; a jittery pane (updated, not-updated, updated) never
// accumulates enough consecutive ticks to flip.
func TestStatusReconcile_HysteresisHoldsReadyOnJitter(t *testing.T) {
	inst := mustStartedInstance(t, "w1", "w1", session.Ready)
	h := newStatusHome(t, inst)

	// Tick 1: pane "changed" (chrome jitter), adapter says Working. Without
	// hysteresis this would flip to Running immediately.
	model, _ := h.Update(tickMetadata(inst, true, program.StatusWorking, 0))
	h = model.(*home)
	assert.Equal(t, session.Ready, inst.Status, "a single working tick must not demote Ready")

	// Tick 2: pane stable (no chrome change this tick), adapter still Working.
	// A stable tick resets the working streak, so the next jitter tick starts
	// counting from 1 again.
	model, _ = h.Update(tickMetadata(inst, false, program.StatusWorking, 100*time.Millisecond))
	h = model.(*home)
	assert.Equal(t, session.Ready, inst.Status, "stable tick keeps Ready")
	assert.Equal(t, 0, h.workingStreak[inst.GetID()], "stable tick resets the working streak")

	// Tick 3: jitter again. Streak is 1, still below threshold.
	model, _ = h.Update(tickMetadata(inst, true, program.StatusWorking, 0))
	h = model.(*home)
	assert.Equal(t, session.Ready, inst.Status, "streak=1 must not demote Ready")
}

// TestStatusReconcile_HysteresisFlipsAfterThreshold proves that sustained
// "really working" ticks (the agent genuinely resumed work) DO eventually flip
// Ready→Running once the streak exceeds the threshold. The hysteresis delays
// the flip, it doesn't prevent it.
func TestStatusReconcile_HysteresisFlipsAfterThreshold(t *testing.T) {
	inst := mustStartedInstance(t, "w1", "w1", session.Ready)
	h := newStatusHome(t, inst)

	// Drive `readyToWorkingTicks` consecutive working ticks. The instance
	// must hold Ready until the threshold, then flip to Running.
	for i := 1; i <= readyToWorkingTicks; i++ {
		model, _ := h.Update(tickMetadata(inst, true, program.StatusWorking, 0))
		h = model.(*home)
		if i < readyToWorkingTicks {
			assert.Equal(t, session.Ready, inst.Status,
				"tick %d (< threshold %d) must hold Ready", i, readyToWorkingTicks)
		}
	}
	assert.Equal(t, session.Running, inst.Status,
		"after %d consecutive working ticks the instance flips to Running", readyToWorkingTicks)
}

// TestStatusReconcile_ReadySignalResetsStreak proves an authoritative Ready
// signal (adapter StatusReady, e.g. the Pi sentinel scrolling back into view)
// resets the working streak, so the next working phase starts counting from
// 1. Without this, a sentinel that flickers in/out of the captured viewport
// would accumulate a streak across the "ready" gaps and eventually false-flip.
func TestStatusReconcile_ReadySignalResetsStreak(t *testing.T) {
	inst := mustStartedInstance(t, "w1", "w1", session.Ready)
	h := newStatusHome(t, inst)

	// Accumulate 2 working ticks (below the threshold).
	for i := 0; i < 2; i++ {
		model, _ := h.Update(tickMetadata(inst, true, program.StatusWorking, 0))
		h = model.(*home)
	}
	require.Equal(t, 2, h.workingStreak[inst.GetID()])
	assert.Equal(t, session.Ready, inst.Status)

	// Sentinel reappears: StatusReady resets the streak.
	model, _ := h.Update(tickMetadata(inst, true, program.StatusReady, 0))
	h = model.(*home)
	assert.Equal(t, session.Ready, inst.Status)
	assert.Equal(t, 0, h.workingStreak[inst.GetID()], "a Ready signal resets the working streak")

	// One working tick after the reset: streak is 1, still below threshold.
	model, _ = h.Update(tickMetadata(inst, true, program.StatusWorking, 0))
	h = model.(*home)
	assert.Equal(t, session.Ready, inst.Status, "streak reset means we start counting from 1 again")
}

// TestStatusReconcile_RunningStaysRunningNoHysteresis proves the hysteresis
// only gates the Ready→Running transition. A Running instance stays Running
// on a single working tick (no need to accumulate a streak from Running).
func TestStatusReconcile_RunningStaysRunningNoHysteresis(t *testing.T) {
	inst := mustStartedInstance(t, "w1", "w1", session.Running)
	h := newStatusHome(t, inst)

	// A working tick on a Running instance: stays Running, no streak games.
	model, _ := h.Update(tickMetadata(inst, true, program.StatusWorking, 0))
	h = model.(*home)
	assert.Equal(t, session.Running, inst.Status)
	assert.Equal(t, 0, h.workingStreak[inst.GetID()], "no streak accumulated from a Running instance")
}

// TestStatusReconcile_StableFallbackStillFlipsToReady proves the
// agent-agnostic stability fallback (stableFor >= threshold → Ready) still
// works alongside the hysteresis: a long-stable pane flips to Ready and
// resets the streak.
func TestStatusReconcile_StableFallbackStillFlipsToReady(t *testing.T) {
	inst := mustStartedInstance(t, "w1", "w1", session.Running)
	h := newStatusHome(t, inst)

	model, _ := h.Update(tickMetadata(inst, false, program.StatusUnknown, stableReadyThreshold+time.Second))
	h = model.(*home)
	assert.Equal(t, session.Ready, inst.Status, "stable fallback flips to Ready")
	assert.Equal(t, 0, h.workingStreak[inst.GetID()], "stable fallback resets the streak")
}
