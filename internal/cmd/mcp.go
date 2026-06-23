package cmd

import (
	"github.com/spf13/cobra"

	"github.com/cybernagle/memory-cli/internal/config"
	"github.com/cybernagle/memory-cli/internal/mcp"
)

// mcpCmd starts the MCP server (stdio JSON-RPC). Clients like Claude Code connect via:
//
//	"mcpServers": {"memory": {"command": "memory", "args": ["mcp"]}}
var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start MCP server (stdio) for Claude Code / Cursor / makro integration",
	Long: `Start the Model Context Protocol server.

Exposes 6 tools via stdio JSON-RPC:
  memory_ask      — ask a question (with intent detection + workflow)
  memory_search   — search memories by keyword
  memory_write    — write a new memory
  memory_timeline — timeline summary for a day/range
  memory_list     — list memories by phase/category/project
  memory_remind   — create a time-based reminder

Configure in Claude Code (~/.claude/claude_desktop_config.json or project .mcp.json):
  {
    "mcpServers": {
      "memory": {"command": "memory", "args": ["mcp"]}
    }
  }`,
	RunE: runMCP,
}

func init() {
	rootCmd.AddCommand(mcpCmd)
}

func runMCP(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	server, err := mcp.NewServer(cfg)
	if err != nil {
		return err
	}
	return server.Run()
}
