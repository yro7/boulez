package daemon

import (
	"testing"
	"time"

	"github.com/yro7/boulez/notify"
	"github.com/yro7/boulez/session"
	"github.com/stretchr/testify/assert"
)

// fakeNotifier records the events it receives, for asserting the daemon fires
// the right one on the right transition.
type fakeNotifier struct {
	events []notify.Event
}

func (f *fakeNotifier) Notify(e notify.Event) {
	f.events = append(f.events, e)
}

func TestNotifyReadyTransition_FiresOnRunningToReady(t *testing.T) {
	f := &fakeNotifier{}
	notifyReadyTransition(f, "inst-1", "feature-auth", "local", session.Running, session.Ready)

	require := assert.New(t)
	require.Len(f.events, 1)
	e := f.events[0]
	assert.Equal(t, notify.InstanceReady, e.Kind)
	assert.Equal(t, "inst-1", e.Instance.ID)
	assert.Equal(t, "feature-auth", e.Instance.Title)
	assert.Equal(t, "local", e.Instance.Host)
	assert.False(t, e.At.IsZero())
	assert.WithinDuration(t, time.Now(), e.At, 2*time.Second)
}

func TestNotifyReadyTransition_NoFireWhenAlreadyReady(t *testing.T) {
	// A second consecutive Ready must not re-fire: the transition is
	// Running→Ready, not Ready→Ready. Without this guard the user would get
	// a notification every poll tick while the instance sits idle.
	f := &fakeNotifier{}
	notifyReadyTransition(f, "inst-1", "t", "local", session.Ready, session.Ready)
	assert.Empty(t, f.events)
}

func TestNotifyReadyTransition_NoFireWhenNotReachingReady(t *testing.T) {
	// Ready→Running (user resumed work) is not a "finished" event.
	f := &fakeNotifier{}
	notifyReadyTransition(f, "inst-1", "t", "local", session.Ready, session.Running)
	assert.Empty(t, f.events)
	// Running→Running (still working) is not a transition.
	notifyReadyTransition(f, "inst-1", "t", "local", session.Running, session.Running)
	assert.Empty(t, f.events)
}

func TestNotifyReadyTransition_NilNotifierIsNoop(t *testing.T) {
	// The poll loop always passes a non-nil notifier (Noop when disabled),
	// but the helper must not panic if handed nil — defensive against a
	// future caller that forgets to construct one.
	assert.NotPanics(t, func() {
		notifyReadyTransition(nil, "inst-1", "t", "local", session.Running, session.Ready)
	})
}
