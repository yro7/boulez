package cli

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/yro7/boulez/kernel"
	"github.com/yro7/boulez/log"
	boulezmcp "github.com/yro7/boulez/mcp"
)

// NewMCPCmd builds the `boulez mcp` command tree. Today it has one subcommand:
// `serve`, which runs the Boulez MCP server over stdio, authenticated as a
// given orchestrator instance. A conductor agent (Claude Code, Codex, ...)
// launches this as a subprocess via .mcp.json and discovers the fleet tools.
func NewMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "MCP server exposing the fleet-control surface to a conductor agent",
	}
	cmd.AddCommand(newMCPServeCmd())
	return cmd
}

func newMCPServeCmd() *cobra.Command {
	var as string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the Boulez MCP server over stdio, authenticated as an orchestrator instance",
		Long: `boulez mcp serve runs the Boulez MCP server over the stdio transport,
authenticated as the given orchestrator instance (--as). The conductor agent
that spawned this subprocess discovers the fleet tools via the standard MCP
handshake and calls them; every call is attributed to the orchestrator's plan.

This is normally launched automatically by the conductor via the .mcp.json
written into the orchestrator's control dir at spawn time. You should not need
to run it by hand.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if as == "" {
				return fmt.Errorf("--as <instance-id> is required")
			}
			// The MCP server is a conductor-facing tool surface, not a
			// human-facing one. Log to file only (no stderr noise — stderr
			// is the MCP transport's control channel in some setups).
			log.Initialize(false)
			defer log.Close()

			socketPath, err := kernel.SocketPath()
			if err != nil {
				return fmt.Errorf("resolve socket path: %w", err)
			}
			caller, err := boulezmcp.NewSocketCaller(socketPath, as, EnsureDaemonRunning)
			if err != nil {
				return err
			}
			defer caller.Close()

			server := boulezmcp.NewServer(caller)
			ctx := context.Background()
			return server.Run(ctx, &mcp.StdioTransport{})
		},
	}
	cmd.Flags().StringVar(&as, "as", "", "orchestrator instance ID to authenticate as (required)")
	_ = cmd.MarkFlagRequired("as")
	return cmd
}
