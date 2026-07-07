package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ImplementationName is the MCP server name advertised in the handshake.
const ImplementationName = "boulez"

// Server is the Boulez MCP server. It wraps an mcp.Server from the go-sdk and
// holds the Caller used to forward tool calls to the kernel socket. It is
// stateless beyond the Caller (no fleet state in memory).
type Server struct {
	mcp    *mcp.Server
	caller Caller
}

// NewServer builds a Boulez MCP server with no tools registered. The caller is
// the kernel socket seam (real or fake). Tools are registered by the
// register* functions (tools.go) — NewServer stays minimal so phase 1 can
// assert an empty tools/list before any tool exists.
func NewServer(caller Caller) *Server {
	s := mcp.NewServer(&mcp.Implementation{Name: ImplementationName, Version: "0.0.1"}, nil)
	out := &Server{mcp: s, caller: caller}
	out.registerAll()
	return out
}

// Run serves the MCP protocol on the given transport until ctx is done or the
// transport errors. Production passes &mcp.StdioTransport{}; tests pass an
// in-memory transport.
func (s *Server) Run(ctx context.Context, t mcp.Transport) error {
	return s.mcp.Run(ctx, t)
}
