package mcp_test

import (
	"context"
	"encoding/json"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yro7/boulez/kernel"
	boulezmcp "github.com/yro7/boulez/mcp"
)

// connect starts the Boulez server on an in-memory transport and returns a
// connected client session. Mirrors the go-sdk's own test pattern. The server
// runs in a goroutine; the test tears down via cs.Close().
func connect(t *testing.T, s *boulezmcp.Server) *mcpsdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	ct, st := mcpsdk.NewInMemoryTransports()
	serveErr := make(chan error, 1)
	go func() { serveErr <- s.Run(ctx, st) }()
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	select {
	case err := <-serveErr:
		t.Fatalf("server run failed: %v", err)
	default:
	}
	return cs
}

// TestServer_ListInstances proves the read path end-to-end through the seam:
// the MCP tool call reaches the fake Caller as a list_instances syscall with
// the right params, and the canned summaries render back as JSON text.
func TestServer_ListInstances(t *testing.T) {
	fake := boulezmcp.NewFakeCallerForTest()
	// Stub the kernel's list_instances response: two summaries.
	fake.StubResult("list_instances", mustJSON(t, []kernel.InstanceSummary{
		{ID: "w1", Title: "fix-bug", Repo: "boulez", Branch: "fix-bug", Program: "pi"},
		{ID: "o1", Title: "orchestrator-1"},
	}))

	cs := connect(t, boulezmcp.NewServer(fake))
	defer cs.Close()

	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "list_instances",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %+v", res.Content)
	}
	text := res.Content[0].(*mcpsdk.TextContent).Text
	if !contains(text, "w1") || !contains(text, "o1") {
		t.Fatalf("result missing instance ids: %q", text)
	}

	// Assert the request the fake received: method + params, and that no
	// `caller` field leaked into the wire payload (identity is bound at the
	// socket session, not carried in params).
	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Method != "list_instances" {
		t.Fatalf("expected one list_instances call, got %+v", calls)
	}
	var got map[string]any
	if err := json.Unmarshal(calls[0].Params, &got); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if _, ok := got["caller"]; ok {
		t.Fatalf("params must not carry a caller field: %s", calls[0].Params)
	}
}

// TestServer_ListInstances_Filters verifies optional filters are forwarded as
// wire params (and omitted when nil, so the kernel applies no filter).
func TestServer_ListInstances_Filters(t *testing.T) {
	fake := boulezmcp.NewFakeCallerForTest()
	fake.StubResult("list_instances", mustJSON(t, []kernel.InstanceSummary{}))

	cs := connect(t, boulezmcp.NewServer(fake))
	defer cs.Close()

	kind := "worker"
	if _, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "list_instances",
		Arguments: map[string]any{"kind": kind, "repo": "boulez"},
	}); err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("expected one call, got %d", len(calls))
	}
	var got map[string]any
	if err := json.Unmarshal(calls[0].Params, &got); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if got["kind"] != "worker" {
		t.Fatalf("kind filter not forwarded: %v", got["kind"])
	}
	if got["repo"] != "boulez" {
		t.Fatalf("repo filter not forwarded: %v", got["repo"])
	}
	if _, ok := got["status"]; ok {
		t.Fatalf("status should be omitted when unset, got %v", got["status"])
	}
}

// TestServer_GetInstance proves the read-detail path: the tool emits a
// get_instance syscall with the id, and the full InstanceDetail (including
// diff stats) renders back as indented JSON.
func TestServer_GetInstance(t *testing.T) {
	fake := boulezmcp.NewFakeCallerForTest()
	fake.StubResult("get_instance", mustJSON(t, kernel.InstanceDetail{
		InstanceSummary: kernel.InstanceSummary{ID: "w1", Title: "fix-bug", Repo: "boulez"},
		Log:             "scrollback line\n",
	}))

	cs := connect(t, boulezmcp.NewServer(fake))
	defer cs.Close()

	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "get_instance",
		Arguments: map[string]any{"id": "w1"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %+v", res.Content)
	}
	text := res.Content[0].(*mcpsdk.TextContent).Text
	if !contains(text, "w1") || !contains(text, "fix-bug") || !contains(text, "scrollback line") {
		t.Fatalf("result missing detail fields: %q", text)
	}

	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Method != "get_instance" {
		t.Fatalf("expected one get_instance call, got %+v", calls)
	}
	var got map[string]any
	if err := json.Unmarshal(calls[0].Params, &got); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if got["id"] != "w1" {
		t.Fatalf("id not forwarded: %v", got["id"])
	}
}

// TestServer_SpawnWorker proves the mutate path: the tool emits a
// spawn_worker syscall with the forwarded args, and returns the new instance
// ID. Critically, it asserts NO `caller` field leaks into the wire params —
// identity is bound at the socket session (authenticate), not carried in
// params (a client cannot spoof another instance to bypass the recursion
// guard; the MCP surface must not even mention caller).
func TestServer_SpawnWorker(t *testing.T) {
	fake := boulezmcp.NewFakeCallerForTest()
	fake.StubResult("spawn_worker", mustJSON(t, map[string]string{"id": "w42"}))

	cs := connect(t, boulezmcp.NewServer(fake))
	defer cs.Close()

	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name: "spawn_worker",
		Arguments: map[string]any{
			"repo":    "/path/to/boulez",
			"prompt":  "fix the bug",
			"program": "pi",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool returned error: %+v", res.Content)
	}
	text := res.Content[0].(*mcpsdk.TextContent).Text
	if !contains(text, "w42") {
		t.Fatalf("result missing new id: %q", text)
	}

	calls := fake.Calls()
	if len(calls) != 1 || calls[0].Method != "spawn_worker" {
		t.Fatalf("expected one spawn_worker call, got %+v", calls)
	}
	var got map[string]any
	if err := json.Unmarshal(calls[0].Params, &got); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	if got["repo"] != "/path/to/boulez" {
		t.Fatalf("repo not forwarded: %v", got["repo"])
	}
	if got["prompt"] != "fix the bug" {
		t.Fatalf("prompt not forwarded: %v", got["prompt"])
	}
	if _, ok := got["caller"]; ok {
		t.Fatalf("params must not carry a caller field: %s", calls[0].Params)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
