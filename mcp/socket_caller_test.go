package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/yro7/boulez/kernel"
)

// fakeSocketServer is a minimal kernel-socket stand-in: a unix-socket server
// speaking the newline-delimited JSON-RPC protocol. It records the requests it
// receives and replies with canned responses by method. This lets SocketCaller
// be tested end-to-end at the wire level without a real kernel/daemon.
type fakeSocketServer struct {
	t        *testing.T
	listener net.Listener
	path     string
	replies  map[string]json.RawMessage // method -> JSON result
	errReps  map[string]*kernel.ErrorInfo
	authID   string
}

func newFakeSocketServer(t *testing.T) *fakeSocketServer {
	t.Helper()
	// Use a short path: macOS sockaddr_un limit is ~104 bytes, and
	// t.TempDir() paths are long. Put the socket directly in the OS temp
	// dir with a short unique name.
	f, err := os.CreateTemp("", "bzl-*.sock")
	if err != nil {
		t.Fatalf("temp socket: %v", err)
	}
	path := f.Name()
	f.Close()
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fs := &fakeSocketServer{
		t:        t,
		listener: l,
		path:     path,
		replies:  map[string]json.RawMessage{},
		errReps:  map[string]*kernel.ErrorInfo{},
	}
	go fs.serve()
	t.Cleanup(func() { l.Close() })
	return fs
}

func (f *fakeSocketServer) stubReply(method string, result json.RawMessage) {
	f.replies[method] = result
}

func (f *fakeSocketServer) stubError(method string, err *kernel.ErrorInfo) {
	f.errReps[method] = err
}

func (f *fakeSocketServer) serve() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go f.handle(conn)
	}
}

func (f *fakeSocketServer) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		var req kernel.Request
		if err := json.Unmarshal(line, &req); err != nil {
			f.t.Errorf("fake server: unmarshal request: %v", err)
			return
		}
		if req.Method == "authenticate" {
			var p struct {
				ID string `json:"instance_id"`
			}
			_ = json.Unmarshal(req.Params, &p)
			f.authID = p.ID
		}
		var resp kernel.Response
		if e, ok := f.errReps[req.Method]; ok {
			resp.Error = e
		} else if r, ok := f.replies[req.Method]; ok {
			resp.Result = r
		} else {
			resp.Result = json.RawMessage("null")
		}
		out, _ := json.Marshal(resp)
		if _, err := conn.Write(append(out, '\n')); err != nil {
			return
		}
	}
}

// TestSocketCaller_AuthenticateAndCall proves the wire-level contract: the
// SocketCaller dials, sends authenticate with the instance id, then forwards a
// syscall on the SAME persistent connection and decodes the result.
func TestSocketCaller_AuthenticateAndCall(t *testing.T) {
	srv := newFakeSocketServer(t)
	srv.stubReply("authenticate", json.RawMessage(`{"ok":true}`))
	srv.stubReply("list_instances", json.RawMessage(`[{"ID":"w1"}]`))

	caller, err := NewSocketCaller(srv.path, "orch-42", nil)
	if err != nil {
		t.Fatalf("NewSocketCaller: %v", err)
	}
	defer caller.Close()

	if srv.authID != "orch-42" {
		t.Fatalf("authenticate sent id %q, want orch-42", srv.authID)
	}

	raw, errInfo, err := caller.Call(context.Background(), "list_instances", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if errInfo != nil {
		t.Fatalf("unexpected error: %+v", errInfo)
	}
	if !strings.Contains(string(raw), "w1") {
		t.Fatalf("result missing w1: %s", raw)
	}
}

// TestSocketCaller_SurfacesKernelError proves a structured kernel error on the
// wire is decoded into ErrorInfo (stable Code), not a transport error.
func TestSocketCaller_SurfacesKernelError(t *testing.T) {
	srv := newFakeSocketServer(t)
	srv.stubReply("authenticate", json.RawMessage(`{"ok":true}`))
	srv.stubError("merge", &kernel.ErrorInfo{
		Code:    kernel.CodeProtectedBranch,
		Message: "target branch 'main' is protected",
	})

	caller, err := NewSocketCaller(srv.path, "orch-42", nil)
	if err != nil {
		t.Fatalf("NewSocketCaller: %v", err)
	}
	defer caller.Close()

	raw, errInfo, err := caller.Call(context.Background(), "merge", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if raw != nil {
		t.Fatalf("expected nil result on error, got %s", raw)
	}
	if errInfo == nil || errInfo.Code != kernel.CodeProtectedBranch {
		t.Fatalf("expected PROTECTED_BRANCH error, got %+v", errInfo)
	}
}
