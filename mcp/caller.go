// Package mcp exposes a subset of the Boulez kernel control surface as an MCP
// server (Model Context Protocol). It is one of three clients of the kernel
// socket, alongside `boulez ctl` and the TUI. The server is stateless: it
// holds no fleet state in memory and forwards every tool call to the kernel.
//
// The server runs over the stdio MCP transport: a conductor agent (Claude
// Code, Codex, ...) launches `boulez mcp serve --as <orchestrator-id>` as a
// subprocess, discovers the tools via the standard MCP handshake, and calls
// them. The orchestrator identity is bound once at startup (authenticate on
// the kernel socket); the agent never learns its own ID.
//
// This package imports `kernel` (toward stability) and never the reverse: the
// kernel remains agent-neutral and unaware that an MCP layer exists.
package mcp

import (
	"context"
	"encoding/json"

	"github.com/yro7/boulez/kernel"
)

// Caller is the seam between the MCP server and the kernel control socket.
// Production uses a socket-backed implementation (reuses kernel.CallSession on
// a persistent authenticated connection); tests inject a fake that records
// emitted Requests.
//
// The caller identity is already bound at NewServer time (via authenticate on
// the socket session). Call must NOT inject a `caller` field into params —
// the kernel ignores it, but the MCP surface must not even mention it.
type Caller interface {
	// Call issues one authenticated syscall. method is a kernel syscall name
	// (e.g. "list_instances", "spawn_worker"). params is the JSON-encoded
	// payload (method-specific), or nil for parameterless calls. It returns
	// the JSON-encoded result, or the structured kernel error (with a stable
	// Code the agent can branch on), or a transport-level error.
	Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *kernel.ErrorInfo, error)
}

// fakeCaller is a test double for Caller. It records every Request it received
// (in order) and returns canned responses keyed by method. Zero-value
// responses (nil result, nil error) are returned for any method without a
// canned entry, so tests only stub what they assert on.
type fakeCaller struct {
	calls    []kernel.Request
	results  map[string]json.RawMessage // method -> JSON result
	errInfos map[string]*kernel.ErrorInfo
}

func newFakeCaller() *fakeCaller {
	return &fakeCaller{
		results:  make(map[string]json.RawMessage),
		errInfos: make(map[string]*kernel.ErrorInfo),
	}
}

// NewFakeCallerForTest exposes a fake Caller for external tests. It records
// every Request it receives and returns canned responses keyed by method.
// Production code must not use this.
func NewFakeCallerForTest() *fakeCaller {
	return newFakeCaller()
}

// StubResult sets the JSON result returned for the given method.
func (f *fakeCaller) StubResult(method string, result json.RawMessage) {
	f.results[method] = result
}

func (f *fakeCaller) StubError(method string, err *kernel.ErrorInfo) {
	f.errInfos[method] = err
}

// Calls returns a copy of the requests received, in order.
func (f *fakeCaller) Calls() []kernel.Request {
	out := make([]kernel.Request, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeCaller) Call(_ context.Context, method string, params json.RawMessage) (json.RawMessage, *kernel.ErrorInfo, error) {
	f.calls = append(f.calls, kernel.Request{Method: method, Params: params})
	if err, ok := f.errInfos[method]; ok {
		return nil, err, nil
	}
	if res, ok := f.results[method]; ok {
		return res, nil, nil
	}
	return json.RawMessage("null"), nil, nil
}
