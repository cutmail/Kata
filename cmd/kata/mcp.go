package main

import (
	"github.com/spf13/cobra"

	"github.com/cutmail/kata/internal/mcpserver"
)

// newMCPCmd は他のコマンドと違い、マニフェストを開いて即座に何かするのではなく、
// MCP サーバーとして標準入出力に居座り続ける。そのため main.go には混ぜず独立させる。
func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run kata as an MCP server over stdio",
		Long: "Expose kata's operations (init, add, sync, list, status, import, update, doctor,\n" +
			"prune, remove) as MCP tools over stdio, so an AI agent can call kata directly\n" +
			"instead of shelling out to the CLI.\n\n" +
			"Each tool accepts an optional 'dir' argument used to locate kata.yml, the same\n" +
			"way the CLI locates it from the current directory; when omitted it defaults to\n" +
			"this process's own working directory.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return mcpserver.Run(cmd.Context(), version)
		},
	}
}
