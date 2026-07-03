// Package kernel transport: a thin JSON-RPC (newline-delimited) server over a
// unix socket, layered over Kernel. The kernel itself is pure Go methods;
// this file adds the wire. No business logic lives here — it decodes a
// Request, dispatches to the matching Kernel method, encodes the Response.
//
// This is the canonical control channel: \`cs2 ctl\` (step 6 client) speaks
// this protocol, and a future LLM's tools will speak it too. cs2 does not
// know who is calling.
package kernel

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"

	"claude-squad/config"
	"claude-squad/session"
	"claude-squad/session/git"
)

// SocketPath returns the path to the kernel control socket under the cs2
// config dir. Stable across restarts so the client can find it.
func SocketPath() (string, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return "", err
	}
	return configDir + "/ctl.sock", nil
}

// Request is one syscall invocation on the wire.
type Request struct {
	// Method is the syscall name (e.g. "spawn_worker", "list_instances").
	Method string `json:"method"`
	// Params is the method-specific payload (decoded by dispatch).
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the reply. Exactly one of Result or Error is set.
type Response struct {
	// Result is the JSON-encoded success payload.
	Result json.RawMessage `json:"result,omitempty"`
	// Error is set when the syscall failed. Code is a machine-readable
	// stable string (e.g. "PROTECTED_BRANCH", "UNKNOWN_INSTANCE") so the
	// client can branch on it without parsing the message.
	Error *ErrorInfo `json:"error,omitempty"`
}

// ErrorInfo is the structured error payload.
type ErrorInfo struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// codes for common kernel errors. Stable so clients (and LLM tools) can
// branch on them.
const (
	CodeUnknownInstance    = "UNKNOWN_INSTANCE"
	CodeWorkerCannotSpawn  = "WORKER_CANNOT_SPAWN"
	CodeNestedOrchestrator = "NESTED_ORCHESTRATOR"
	CodeProtectedBranch    = "PROTECTED_BRANCH"
	CodeInternal           = "INTERNAL"
)

// Serve listens on the control socket and dispatches requests to the kernel.
// It blocks until the listener closes. The socket is removed before binding
// (stale socket from a crashed daemon) and on shutdown.
func Serve(k *Kernel, socketPath string) error {
	// Remove a stale socket from a previous crash.
	_ = os.Remove(socketPath)

	l, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("kernel: listen %s: %w", socketPath, err)
	}
	defer func() { _ = l.Close() }()
	// Restrict to the owning user: control is privileged (it can spawn/merge).
	_ = os.Chmod(socketPath, 0o600)

	for {
		conn, err := l.Accept()
		if err != nil {
			return nil // listener closed
		}
		go handleConn(k, conn)
	}
}

// handleConn serves one client connection: read newline-delimited requests,
// dispatch each, write newline-delimited responses. A connection may carry
// multiple requests (the client may pipeline). Each connection owns a
// session — the authoritative caller identity. A session starts
// unauthenticated (top-level, like `cs2 ctl`) and may bind to an instance
// via the `authenticate` syscall. The caller identity is derived from the
// session by the transport, NEVER from request params — so a client cannot
// spoof another instance's identity to bypass the recursion guard.
func handleConn(k *Kernel, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	sess := k.newSession()
	defer k.releaseSession(sess)
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // raise for large diffs/logs
	enc := json.NewEncoder(conn)
	for scanner.Scan() {
		var req Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			_ = enc.Encode(Response{Error: &ErrorInfo{Code: CodeInternal, Message: "bad json: " + err.Error()}})
			continue
		}
		resp := dispatch(k, sess, req)
		_ = enc.Encode(resp)
	}
}

// dispatch maps a Request to a Kernel method. This is the only place method
// names live — the canonical table. Adding a syscall = one case here + one
// Kernel method. The caller identity comes from the session (authoritative),
// not from request params — a client cannot declare a caller identity.
func dispatch(k *Kernel, sess *ctlSession, req Request) Response {
	switch req.Method {
	case "authenticate":
		var p struct {
			ID   string        `json:"instance_id"`
			Kind session.Kind  `json:"kind"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(CodeInternal, "bad params: "+err.Error())
		}
		if err := k.BindCaller(sess, p.ID, p.Kind); err != nil {
			return kernelErrResp(err)
		}
		return okResp(map[string]bool{"ok": true})
	case "list_instances":
		var p listParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(CodeInternal, "bad params: "+err.Error())
		}
		summaries := k.ListInstances(p.toFilter())
		return okResp(summaries)
	case "get_instance":
		var p struct{ ID string `json:"id"` }
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(CodeInternal, "bad params: "+err.Error())
		}
		d, err := k.GetInstance(p.ID)
		if err != nil {
			return kernelErrResp(err)
		}
		return okResp(d)
	case "spawn_worker":
		var p spawnParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(CodeInternal, "bad params: "+err.Error())
		}
		// The caller comes from the session (authoritative), not from the
		// request's `caller` field. A client cannot declare a caller identity
		// to bypass the recursion guard — the kernel binds it via
		// `authenticate`. The `caller` field in params is ignored (kept in the
		// struct for back-compat with older clients, but never read).
		id, err := k.Spawn(k.callerFor(sess), p.toOptions())
		if err != nil {
			return kernelErrResp(err)
		}
		return okResp(map[string]string{"id": id})
	case "send_prompt":
		var p struct {
			ID     string `json:"id"`
			Prompt string `json:"prompt"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(CodeInternal, "bad params: "+err.Error())
		}
		if err := k.SendPrompt(p.ID, p.Prompt); err != nil {
			return kernelErrResp(err)
		}
		return okResp(map[string]bool{"ok": true})
	case "pause":
		return simpleByID(k, "pause", func(id string) error { return k.Pause(id) }, req.Params)
	case "resume":
		return simpleByID(k, "resume", func(id string) error { return k.Resume(id) }, req.Params)
	case "kill":
		return simpleByID(k, "kill", func(id string) error { return k.Kill(id) }, req.Params)
	case "merge":
		var p mergeParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(CodeInternal, "bad params: "+err.Error())
		}
		// Caller from the session, not from request params (see spawn_worker).
		res, err := k.Merge(k.callerFor(sess), p.TargetRepo, p.TargetBranch, p.SourceBranches, git.Strategy(p.Strategy))
		if err != nil {
			return kernelErrResp(err)
		}
		return okResp(res)
	default:
		return errResp(CodeInternal, "unknown method: "+req.Method)
	}
}

// simpleByID handles the {id}-only mutations (pause/resume/kill).
func simpleByID(k *Kernel, _ string, fn func(id string) error, raw json.RawMessage) Response {
	var p struct{ ID string `json:"id"` }
	if err := json.Unmarshal(raw, &p); err != nil {
		return errResp(CodeInternal, "bad params: "+err.Error())
	}
	if err := fn(p.ID); err != nil {
		return kernelErrResp(err)
	}
	return okResp(map[string]bool{"ok": true})
}

// --- wire param structs ---

type listParams struct {
	Kind   *session.Kind `json:"kind,omitempty"`
	Status *session.Status `json:"status,omitempty"`
	Repo   string        `json:"repo,omitempty"`
}

func (p listParams) toFilter() ListFilter {
	f := ListFilter{}
	if p.Kind != nil {
		f = composeKindFilter(f, *p.Kind)
	}
	if p.Status != nil {
		f = composeStatusFilter(f, *p.Status)
	}
	f.repo = p.Repo
	return f
}

// composeKindFilter sets the kind dimension on a filter (handles the zero-value
// KindWorker case via the kindSet bool).
func composeKindFilter(f ListFilter, k session.Kind) ListFilter {
	f.kind = k
	f.kindSet = true
	return f
}

func composeStatusFilter(f ListFilter, s session.Status) ListFilter {
	f.status = s
	f.statusSet = true
	return f
}

type spawnParams struct {
	Repo    string        `json:"repo"`
	Branch  string        `json:"branch,omitempty"`
	Prompt  string        `json:"prompt,omitempty"`
	Program string        `json:"program,omitempty"`
	Title   string        `json:"title,omitempty"`
	Kind    session.Kind  `json:"kind,omitempty"`
	Caller  callerParams   `json:"caller,omitempty"`
}

func (p spawnParams) toOptions() SpawnOptions {
	return SpawnOptions{
		Repo:    p.Repo,
		Branch:  p.Branch,
		Prompt:  p.Prompt,
		Program: p.Program,
		Title:   p.Title,
		Kind:    p.Kind,
	}
}

// callerParams is the DEPRECATED client-declared caller. It is retained in
// the wire structs for back-compat with older clients but is NEVER read by
// the transport — the caller identity is derived from the session (bound via
// `authenticate`), so a client cannot spoof another instance to bypass the
// recursion guard. Kept here so an old client sending `caller` doesn't
// break the JSON unmarshal.
type callerParams struct {
	ID   string        `json:"id,omitempty"`
	Kind session.Kind `json:"kind,omitempty"`
}

// toContext is retained for tests that construct a CallerContext directly;
// the transport no longer calls it.
func (c callerParams) toContext() CallerContext {
	return CallerContext{CallerID: c.ID, Kind: c.Kind}
}

type mergeParams struct {
	Caller         callerParams `json:"caller,omitempty"`
	TargetRepo     string       `json:"target_repo"`
	TargetBranch   string       `json:"target_branch"`
	SourceBranches []string     `json:"source_branches"`
	Strategy       int          `json:"strategy,omitempty"`
}

// --- response helpers ---

func okResp(v interface{}) Response {
	b, err := json.Marshal(v)
	if err != nil {
		return errResp(CodeInternal, "marshal result: "+err.Error())
	}
	return Response{Result: b}
}

func errResp(code, msg string) Response {
	return Response{Error: &ErrorInfo{Code: code, Message: msg}}
}

// kernelErrResp maps a kernel error to its wire code.
func kernelErrResp(err error) Response {
	switch err.(type) {
	case ErrUnknownInstance:
		return errResp(CodeUnknownInstance, err.Error())
	case ErrWorkerCannotSpawn:
		return errResp(CodeWorkerCannotSpawn, err.Error())
	case ErrNestedOrchestrator:
		return errResp(CodeNestedOrchestrator, err.Error())
	case git.ErrProtectedBranch:
		return errResp(CodeProtectedBranch, err.Error())
	default:
		return errResp(CodeInternal, err.Error())
	}
}

// --- client ---

// Call sends one Request to the kernel over the control socket and returns
// the Response. This is the entire client: \`cs2 ctl\` wraps it. Synchronous
// req→resp, as an LLM needs (e.g. spawn → {id}).
func Call(socketPath string, req Request) (Response, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return Response{}, fmt.Errorf("dial kernel socket %s: %w (is the daemon running?)", socketPath, err)
	}
	defer func() { _ = conn.Close() }()

	line, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("marshal request: %w", err)
	}
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return Response{}, fmt.Errorf("write request: %w", err)
	}

	reader := bufio.NewReader(conn)
	respLine, err := reader.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return Response{}, fmt.Errorf("read response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return Response{}, fmt.Errorf("unmarshal response: %w", err)
	}
	return resp, nil
}

// CallSession sends a sequence of requests on a SINGLE connection and returns
// the responses in order. This is how a client authenticates then issues a
// syscall in the same session: `authenticate` binds the connection to an
// instance, and the subsequent request is attributed to that instance. A
// one-shot `Call` can't do this because each Call is a fresh connection
// (unauthenticated top-level). Used by `cs2 ctl as <id> ...` and by tests.
func CallSession(socketPath string, reqs []Request) ([]Response, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial kernel socket %s: %w (is the daemon running?)", socketPath, err)
	}
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)
	resps := make([]Response, 0, len(reqs))
	for _, req := range reqs {
		line, err := json.Marshal(req)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		if _, err := conn.Write(append(line, '\n')); err != nil {
			return nil, fmt.Errorf("write request: %w", err)
		}
		respLine, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("read response: %w", err)
		}
		var resp Response
		if err := json.Unmarshal(respLine, &resp); err != nil {
			return nil, fmt.Errorf("unmarshal response: %w", err)
		}
		resps = append(resps, resp)
	}
	return resps, nil
}
