package daemon

import (
	"time"

	"github.com/yro7/boulez/notify"
	"github.com/yro7/boulez/session"
)

// notifyReadyTransition fires an InstanceReady notification when an instance
// transitions to Ready. prev is the status before this poll's stabilization,
// stable is the stabilized result. This is the canonical detection point:
// the daemon already computes both values to drive UpdateStatus — here it
// also feeds the notifier, so the transition is detected exactly once (in the
// daemon) rather than re-derived by every consumer.
//
// Best-effort: the notifier owns its error handling and MUST NOT propagate
// failures. A nil notifier is a silent no-op (defensive; the poll loop
// always passes a non-nil notifier, Noop when disabled).
func notifyReadyTransition(n notify.Notifier, id, title, hostName string, prev, stable session.Status) {
	if n == nil {
		return
	}
	if prev == session.Ready || stable != session.Ready {
		return
	}
	n.Notify(notify.Event{
		Kind: notify.InstanceReady,
		Instance: notify.InstanceInfo{
			ID:    id,
			Title: title,
			Host:  hostName,
		},
		At: time.Now(),
	})
}
