package session

import (
	"time"

	"github.com/yro7/boulez/program"
)

// StableReadyThreshold is the agent-agnostic fallback: when an adapter returns
// no authoritative ready signal (StatusUnknown, or StatusWorking with a pane
// that has not changed), an instance whose observation source has been stable
// for this long is presumed idle (waiting on input or a permission, not
// streaming). An adapter's explicit StatusReady always takes priority.
const StableReadyThreshold = 60 * time.Second

// ReadyToWorkingTicks is the hysteresis threshold for the Ready→Running
// transition. An agent's pane chrome (animated spinner, context-usage
// percentage, cursor) keeps hashing differently even when the agent is idle,
// so without hysteresis an idle instance flickers Ready↔Running every poll
// tick. The counter accumulates consecutive "really working" ticks (observation
// source changed AND no authoritative Ready signal) and only flips
// Ready→Running once it reaches this threshold. A Ready signal or a stable
// observation source resets it.
const ReadyToWorkingTicks = 3

// Stabilize de-noises a single observation of an agent's state into a stable
// session.Status. It is the authority's (the daemon's) status-reconciliation
// step: given what the adapter just observed (observed/updated/stableFor) and
// the instance's previous status, it returns the status the instance should
// now hold.
//
// streaks is the per-instance hysteresis counter map (keyed by instance ID);
// Stabilize mutates it in place. The caller owns the map's lifecycle (creation,
// pruning of gone instances) — Stabilize only reads/writes the entry for the
// given id.
//
// Mapping (port of the former TUI status-reconciliation loop in app/app.go):
//   - StatusReady (authoritative)        → Ready, reset streak.
//   - StatusPermission                    → unchanged (a permission decision is
//     not free-input "ready"; the caller resolves the prompt separately).
//   - StatusWorking/StatusUnknown, source changed:
//   - Ready prev: hold Ready until streak reaches ReadyToWorkingTicks, then
//     flip to Running (chrome jitter no longer causes flicker).
//   - other prev: Running, clear streak.
//   - source unchanged, stableFor >= StableReadyThreshold → Ready, reset streak.
//   - source unchanged, not stable long enough:
//   - Ready prev: hold Ready, reset streak.
//   - other prev: keep prev (Running), clear streak.
//
// For a journaling adapter (e.g. Pi) the observation source is deterministic
// (no chrome jitter), so the hysteresis is a harmless no-op: a Ready signal
// flips immediately and a Working signal accumulates the streak trivially.
func Stabilize(streaks map[string]int, id string, observed program.Status, updated bool, stableFor time.Duration, prev Status) Status {
	switch observed {
	case program.StatusReady:
		delete(streaks, id)
		return Ready
	case program.StatusPermission:
		// A resolvable permission/trust prompt: the status is left unchanged
		// (the agent is waiting for a permission decision, not free input).
		// Prompt resolution is the caller's responsibility, gated on AutoYes.
		return prev
	}

	// StatusWorking or StatusUnknown: fall back to observation-source stability.
	if updated {
		if prev == Ready {
			streaks[id]++
			if streaks[id] < ReadyToWorkingTicks {
				return Ready // hold: not enough consecutive working ticks yet
			}
			delete(streaks, id)
			return Running
		}
		delete(streaks, id)
		return Running
	}
	if stableFor >= StableReadyThreshold {
		delete(streaks, id)
		return Ready
	}
	if prev == Ready {
		// Stable tick but not long enough to be idle: hold Ready, reset streak.
		delete(streaks, id)
		return Ready
	}
	delete(streaks, id)
	return prev
}
