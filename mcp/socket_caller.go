package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/yro7/boulez/kernel"
	"github.com/yro7/boulez/session"
)

// SocketCaller is a Caller that speaks the kernel's newline-delimited JSON-RPC
// protocol over a persistent authenticated unix-socket connection. It dials
// once, authenticates as the given instance (so every Call is attributed to
// that orchestrator's plan), and reuses the connection for the server's
// lifetime — unlike kernel.CallSession which is one-shot per call.
//
// It is the production Caller used by `boulez mcp serve`. Tests use fakeCaller
// instead and never touch a real socket.
type SocketCaller struct {
	conn   net.Conn
	reader *bufio.Reader
}

// NewSocketCaller dials the kernel socket, launches the daemon if unreachable
// (delegated to the caller — pass a non-nil launchFunc), and authenticates as
// the given orchestrator instance. The connection stays open until Close.
//
// launchFunc may be nil; when non-nil it is invoked once if the dial fails
// (e.g. cli.EnsureDaemonRunning), then the dial is retried after the socket
// appears. This keeps the daemon-launch policy in the CLI layer, not here.
func NewSocketCaller(socketPath, instanceID string, launchFunc func() error) (*SocketCaller, error) {
	conn, err := dial(socketPath)
	if err != nil {
		if launchFunc != nil {
			if lerr := launchFunc(); lerr != nil {
				return nil, fmt.Errorf("kernel unreachable and auto-launch failed: %w (launch: %v)", err, lerr)
			}
			conn, err = dial(socketPath)
		}
		if err != nil {
			return nil, fmt.Errorf("dial kernel socket %s: %w (is the daemon running?)", socketPath, err)
		}
	}

	sc := &SocketCaller{conn: conn, reader: bufio.NewReader(conn)}
	if err := sc.authenticate(instanceID); err != nil {
		conn.Close()
		return nil, err
	}
	return sc, nil
}

// dial connects to the kernel unix socket.
func dial(socketPath string) (net.Conn, error) {
	return net.Dial("unix", socketPath)
}

// authenticate binds this connection to the orchestrator instance, so all
// subsequent syscalls are attributed to that instance's plan. Mirrors
// `boulez ctl as <id>`'s authenticate step, but on a persistent connection.
// kind is always orchestrator here: the MCP server is only spawned for
// orchestrator instances.
func (c *SocketCaller) authenticate(instanceID string) error {
	params, _ := json.Marshal(struct {
		ID   string       `json:"instance_id"`
		Kind session.Kind `json:"kind"`
	}{ID: instanceID, Kind: session.KindOrchestrator})
	req := kernel.Request{Method: "authenticate", Params: params}
	resp, err := c.roundTrip(req)
	if err != nil {
		return fmt.Errorf("authenticate: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("authenticate: %s: %s", resp.Error.Code, resp.Error.Message)
	}
	return nil
}

// Call issues one authenticated syscall on the persistent connection. The
// method/params are forwarded as-is; no `caller` field is injected (identity
// is bound at authenticate). Safe for concurrent use is NOT guaranteed — the
// MCP server calls tools sequentially via the go-sdk handler, so a single
// in-flight request is the norm. If concurrency is needed later, guard with a
// mutex (the line protocol is request/response on one connection).
func (c *SocketCaller) Call(_ context.Context, method string, params json.RawMessage) (json.RawMessage, *kernel.ErrorInfo, error) {
	resp, err := c.roundTrip(kernel.Request{Method: method, Params: params})
	if err != nil {
		return nil, nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error, nil
	}
	return resp.Result, nil, nil
}

// roundTrip sends one request line and reads one response line. Mirrors
// kernel.CallSession's per-request loop, but on the persistent connection.
func (c *SocketCaller) roundTrip(req kernel.Request) (kernel.Response, error) {
	line, err := json.Marshal(req)
	if err != nil {
		return kernel.Response{}, fmt.Errorf("marshal request: %w", err)
	}
	if _, err := c.conn.Write(append(line, '\n')); err != nil {
		return kernel.Response{}, fmt.Errorf("write request: %w", err)
	}
	respLine, err := c.reader.ReadBytes('\n')
	if err != nil {
		return kernel.Response{}, fmt.Errorf("read response: %w", err)
	}
	var resp kernel.Response
	if err := json.Unmarshal(respLine, &resp); err != nil {
		return kernel.Response{}, fmt.Errorf("unmarshal response: %w", err)
	}
	return resp, nil
}

// Close releases the kernel socket connection.
func (c *SocketCaller) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
