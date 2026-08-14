// Package mcpserver は kata の操作を Model Context Protocol のツールとして公開する。
//
// cmd/kata が cobra 経由で internal/app を呼ぶのと同じように、ここでは MCP の
// ツール呼び出し経由で internal/app を呼ぶ。ビジネスロジックは一切持たない薄い層。
//
// ハンドラが error を返すと SDK が自動的に CallToolResult{IsError: true} に変換して
// 返す（プロトコルエラーにはならない）ため、呼び出し元のエージェントは通常のツール
// 結果として失敗を読める。
package mcpserver

import (
	"context"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Run は kata の操作を公開する MCP サーバーを標準入出力で起動する。
func Run(ctx context.Context, version string) error {
	server := NewServer(version)
	return server.Run(ctx, &mcp.StdioTransport{})
}

// NewServer はツールを登録済みの MCP サーバーを返す。トランスポートには結び付けない
// ため、テストは stdio を経由せず InMemoryTransport で直接つなげられる。
func NewServer(version string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "kata", Version: version}, nil)
	registerTools(server)
	return server
}

// resolveDir は dir が空のときサーバープロセス自身の作業ディレクトリで補う。
//
// CLI の openApp() が os.Getwd() を起点にするのと同じ既定を、MCP 越しでも
// 再現するため。エージェントが特定のプロジェクトを指したいときだけ dir を渡せばよい。
func resolveDir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	return os.Getwd()
}

func ptr[T any](v T) *T { return &v }
