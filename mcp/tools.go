package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yro7/boulez/kernel"
)

// registerAll registers every fleet tool on the server. Called by NewServer.
// Add a tool here = add a register* function below. The set is deliberately a
// subset of the kernel syscalls: `land` (top-level only) and
// `list_instances_full` (TUI-internal) are NOT exposed.
func (s *Server) registerAll() {
	s.registerListInstances()
	s.registerGetInstance()
	s.registerSpawnWorker()
}

// listInstancesArgs is the MCP-facing argument shape for list_instances. It
// uses strings (not session.Kind/session.Status) so the JSON schema exposed to
// the LLM is self-describing; the kernel's UnmarshalJSON accepts these
// strings on the wire. Pointer fields = optional filters (omitted entirely
// when nil so the kernel applies no filter on that dimension).
type listInstancesArgs struct {
	Kind   *string `json:"kind,omitempty"   jsonschema:"filter by instance kind: worker|orchestrator"`
	Status *string `json:"status,omitempty" jsonschema:"filter by status: running|ready|loading|paused"`
	Repo   string  `json:"repo,omitempty"   jsonschema:"filter by repo name"`
}

// listInstancesParams mirrors the kernel's unexported listParams wire shape
// (kind/status/repo). Defined here in the MCP package because listParams is
// unexported; the json tags are identical so the wire contract matches. If the
// kernel ever exports listParams, switch to reusing it.
type listInstancesParams struct {
	Kind   *string `json:"kind,omitempty"`
	Status *string `json:"status,omitempty"`
	Repo   string  `json:"repo,omitempty"`
}

func (s *Server) registerListInstances() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "list_instances",
		Description: "List instances in the fleet, optionally filtered by kind, status, or repo. Each instance is a coding agent in an isolated git worktree. Returns one JSON object per line.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args listInstancesArgs) (*mcp.CallToolResult, any, error) {
		params, err := json.Marshal(listInstancesParams(args))
		if err != nil {
			return nil, nil, fmt.Errorf("marshal list_instances params: %w", err)
		}
		raw, errInfo, err := s.caller.Call(ctx, "list_instances", params)
		if err != nil {
			return nil, nil, err
		}
		if errInfo != nil {
			return errorResult(errInfo), nil, nil
		}
		var summaries []kernel.InstanceSummary
		if err := json.Unmarshal(raw, &summaries); err != nil {
			return nil, nil, fmt.Errorf("unmarshal list_instances result: %w", err)
		}
		return textResult(renderSummaries(summaries)), nil, nil
	})
}

// renderSummaries renders fleet summaries as compact one-line JSON objects, so
// the LLM reads them as a status report rather than a wall of text.
func renderSummaries(in []kernel.InstanceSummary) string {
	if len(in) == 0 {
		return "(no instances match the filter)"
	}
	out := make([]byte, 0, len(in)*128)
	for _, s := range in {
		b, _ := json.Marshal(s)
		out = append(out, b...)
		out = append(out, '\n')
	}
	return string(out)
}

// instanceIDArgs is the shared args shape for single-instance tools: just an ID.
type instanceIDArgs struct {
	ID string `json:"id" jsonschema:"the instance ID"`
}

// get_instance returns the full detail of one instance: status, diff stats,
// and the tmux scrollback (best-effort). This is the projection an
// orchestrator uses to decide what to do with a worker.
func (s *Server) registerGetInstance() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "get_instance",
		Description: "Get full detail of one instance: summary (status, title, repo, branch, program, host), diff stats, and tmux scrollback log. Use this to inspect a worker's progress or diagnose a failure.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args instanceIDArgs) (*mcp.CallToolResult, any, error) {
		params, err := json.Marshal(args)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal get_instance params: %w", err)
		}
		raw, errInfo, err := s.caller.Call(ctx, "get_instance", params)
		if err != nil {
			return nil, nil, err
		}
		if errInfo != nil {
			return errorResult(errInfo), nil, nil
		}
		var detail kernel.InstanceDetail
		if err := json.Unmarshal(raw, &detail); err != nil {
			return nil, nil, fmt.Errorf("unmarshal get_instance result: %w", err)
		}
		b, err := json.MarshalIndent(detail, "", "  ")
		if err != nil {
			return nil, nil, fmt.Errorf("marshal get_instance detail: %w", err)
		}
		return textResult(string(b)), nil, nil
	})
}

// spawnWorkerArgs mirrors kernel.SpawnParams' wire contract (DRY: identical
// json tags) but omits the deprecated Caller field — the MCP surface must not
// even mention caller, since identity is bound at the socket session.
// Required: Repo. The kernel creates the branch from HEAD if Branch is empty
// (deterministic names); BranchMustExist requires it to pre-exist.
type spawnWorkerArgs struct {
	Repo            string `json:"repo"`
	Branch          string `json:"branch,omitempty"          jsonschema:"branch name; created from HEAD if absent"`
	BranchMustExist bool   `json:"branch_must_exist,omitempty" jsonschema:"if true, the branch must pre-exist"`
	Prompt          string `json:"prompt,omitempty"          jsonschema:"the task to give the worker"`
	Program         string `json:"program,omitempty"         jsonschema:"agent program (e.g. pi, claude); defaults to the orchestrator's"`
	Title           string `json:"title,omitempty"            jsonschema:"human-readable instance title"`
	Kind            string `json:"kind,omitempty"            jsonschema:"instance kind: worker (default) | orchestrator"`
	Host            string `json:"host,omitempty"            jsonschema:"host name for multi-env (SSH); empty = local"`
}

func (s *Server) registerSpawnWorker() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "spawn_worker",
		Description: "Spawn a new worker instance in an isolated git worktree of the given repo, running an agent program with the given task prompt. Returns the new instance ID. The worker runs on its own branch and cannot touch the protected trunk.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args spawnWorkerArgs) (*mcp.CallToolResult, any, error) {
		params, err := json.Marshal(args)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal spawn_worker params: %w", err)
		}
		raw, errInfo, err := s.caller.Call(ctx, "spawn_worker", params)
		if err != nil {
			return nil, nil, err
		}
		if errInfo != nil {
			return errorResult(errInfo), nil, nil
		}
		// kernel returns {"id": "..."}
		var res struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, nil, fmt.Errorf("unmarshal spawn_worker result: %w", err)
		}
		return textResult(fmt.Sprintf(`{"id":%q}`, res.ID)), nil, nil
	})
}

// textResult wraps text as the sole content of an MCP tool result.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// errorResult surfaces a structured kernel error (stable Code) as an MCP tool
// result flagged as an error, so the agent can branch on the Code text rather
// than parsing a message.
func errorResult(e *kernel.ErrorInfo) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{
			Text: fmt.Sprintf(`{"code":%q,"message":%s}`, e.Code, jsonString(e.Message)),
		}},
	}
}

// jsonString marshals s as a JSON string (handles quotes/escapes).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
