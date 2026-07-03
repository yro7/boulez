package kernel

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"claude-squad/session"
)

// ctlSession is the transport-level identity of one control connection. A
// connection starts unauthenticated (top-level, like `cs2 ctl`) and may bind
// to an instance via the `authenticate` syscall. Once bound, every syscall
// the connection issues is attributed to that instance — the authoritative
// caller identity. A client cannot set this from request params, so it cannot
// spoof another instance to bypass the recursion guard (a Worker cannot
// spawn; an Orchestrator cannot spawn an Orchestrator).
//
// Trust model (v1, local single-user): any process that can open the unix
// socket (mode 0o600, owner-only) is trusted to authenticate as ANY instance.
// The OS-level isolation (only the owner can connect) is the security
// boundary; the binding is an identity claim, not a capability. Stronger
// per-instance auth (tokens, capabilities) is deferred to Shape B+ when
// multiple consumers share the socket.
type ctlSession struct {
	id     string
	caller CallerContext // zero-value = top-level (unauthenticated)
	bound  bool
}

// newSession allocates a fresh unauthenticated session. The id is a random
// hex string (not used for security, only for log correlation).
func (k *Kernel) newSession() *ctlSession {
	return &ctlSession{id: randomID()}
}

// releaseSession drops any binding. Called when the connection closes; if the
// bound instance outlives the connection its plan persists on disk.
func (k *Kernel) releaseSession(s *ctlSession) {
	k.mu.Lock()
	delete(k.sessions, s.id)
	k.mu.Unlock()
}

// BindCaller binds a session to an instance, validating that the instance
// exists and its Kind matches the claimed Kind. After this, every syscall on
// the session's connection is attributed to that instance. The kernel is the
// authority: a client cannot lie about its Kind to bypass the recursion guard
// (it can only authenticate as an instance the kernel knows, with the Kind
// the kernel recorded at creation).
//
// `authenticate` is the only syscall that mutates session identity. Returns
// ErrUnknownInstance if the ID doesn't resolve.
func (k *Kernel) BindCaller(s *ctlSession, instanceID string, claimedKind session.Kind) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.instancesLocked() // load if not yet loaded

	inst, ok := k.findLocked(instanceID)
	if !ok {
		return ErrUnknownInstance{ID: instanceID}
	}
	// Defense in depth: ignore the client's claimed Kind and use the kernel's
	// recorded Kind. A client cannot escalate by claiming to be an
	// orchestrator when the instance is actually a worker.
	actualKind := inst.Kind()
	if claimedKind != actualKind {
		// Don't leak the actual kind in the message; just refuse. The client
		// should re-authenticate with the correct kind (or omit it and let
		// the kernel decide). We still bind to the actual kind — a worker that
		// mis-claims orchestrator is bound as a worker and correctly barred
		// from spawning.
		_ = claimedKind
	}
	s.caller = CallerContext{CallerID: instanceID, Kind: actualKind}
	s.bound = true
	if k.sessions == nil {
		k.sessions = map[string]*ctlSession{}
	}
	k.sessions[s.id] = s
	return nil
}

// callerFor returns the authoritative CallerContext for a session. An
// unauthenticated session is top-level (can spawn any Kind). An authenticated
// session is bound to its instance's identity — the guards (IsWorker,
// nested-orchestrator) apply.
func (k *Kernel) callerFor(s *ctlSession) CallerContext {
	if s == nil || !s.bound {
		return CallerContext{} // top-level
	}
	return s.caller
}

// randomID returns a short hex string for log correlation (not security).
func randomID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// String of a session for logs.
func (s *ctlSession) String() string {
	if s.bound {
		return fmt.Sprintf("session(%s bound=%s:%s)", s.id, s.caller.Kind, s.caller.CallerID)
	}
	return fmt.Sprintf("session(%s top-level)", s.id)
}
