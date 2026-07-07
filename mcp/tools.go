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

// --- result helpers ---

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
