package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cutmail/kata/internal/safepath"
)

// Local はマニフェストと同じリポジトリに同梱された実体を扱う。
// 自作スキルはこの取得元を使い、実体そのものが git 管理下に置かれる。
type Local struct{}

// NewLocal は Local フェッチャを返す。
func NewLocal() *Local { return &Local{} }

// Fetch は基準ディレクトリからの相対パスを解決して返す。
func (l *Local) Fetch(_ context.Context, req Request) (Fetched, error) {
	if req.BaseDir == "" {
		return Fetched{}, fmt.Errorf("local source requires a base directory")
	}
	joined := filepath.Join(req.BaseDir, req.Local)
	rel, err := filepath.Rel(req.BaseDir, joined)
	if err != nil {
		return Fetched{}, err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Fetched{}, fmt.Errorf("local %q escapes the manifest directory", req.Local)
	}
	abs, err := filepath.Abs(joined)
	if err != nil {
		return Fetched{}, err
	}
	if _, err := os.Lstat(abs); err != nil {
		return Fetched{}, fmt.Errorf("local source %q not found: %w", req.Local, err)
	}
	// マニフェストは clone してきたものでありうる。字句的な検査だけでは、
	// リポジトリにコミットされた symlink 越しにホームの任意の場所を
	// スキルとして配置できてしまう。
	if err := safepath.VerifyUnder(req.BaseDir, abs); err != nil {
		return Fetched{}, fmt.Errorf("local source %q is not usable: %w", req.Local, err)
	}
	if err := safepath.VerifyTree(abs); err != nil {
		return Fetched{}, fmt.Errorf("local source %q cannot be deployed: %w", req.Local, err)
	}
	return Fetched{Root: abs}, nil
}
