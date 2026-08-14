// Package source は取得元の抽象化を提供する。
// 取得元が増えても呼び出し側は Fetcher だけを見ればよい。
package source

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cutmail/kata/internal/safepath"
)

// Request は 1 パッケージ分の取得要求。
type Request struct {
	// Git は取得元 URL。Local と排他。
	Git string
	// Ref はブランチまたはタグ。空ならデフォルトブランチ。
	Ref string
	// Commit は lock で固定済みのコミット。空なら Ref から解決する。
	Commit string
	// Path はリポジトリ内のサブパス。
	Path string
	// Local はリポジトリ同梱の実体への相対パス。Git と排他。
	Local string
	// BaseDir は Local を解決する基準ディレクトリ（kata.yml のある場所）。
	BaseDir string
	// URL は書庫の取得先。Git・Local と排他。
	URL string
	// Digest は取得物に期待する内容ダイジェスト。
	// 空なら検証せず、算出した値を結果として返す。
	Digest string
	// LocalGit は Git が手元のパスを指すことを示す。
	// その場合は BaseDir 配下に収まることを確かめてから取得する。
	LocalGit bool
}

// Fetched は取得結果。
type Fetched struct {
	// Root は配置元となる実体の絶対パス。
	Root string
	// Commit は git 取得時の解決済みコミット。local と url では空。
	Commit string
	// Digest は url 取得時の書庫本体のダイジェスト。git と local では空。
	Digest string
}

// Fetcher は取得元ごとの取得処理。
type Fetcher interface {
	Fetch(ctx context.Context, req Request) (Fetched, error)
}

// resolveSubpath は取得したツリーの中からサブパスを解決する。
//
// 字句的な検査だけでは足りない。取得元が経路の途中に symlink を仕込めば、
// root の内側に見えるパスがホームの任意の場所を指しうる。そのため経路の各要素と
// 解決した先のツリーの両方に symlink が無いことまで確かめる。
// 書庫の展開が symlink を拒否しているのと同じ規則を、git と local にも適用する。
func resolveSubpath(root, sub string) (string, error) {
	joined := root
	if sub != "" {
		joined = filepath.Join(root, sub)
		rel, err := filepath.Rel(root, joined)
		if err != nil {
			return "", err
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q escapes the source root", sub)
		}
		if err := safepath.VerifyUnder(root, joined); err != nil {
			return "", fmt.Errorf("path %q is not usable: %w", sub, err)
		}
	}
	if _, err := os.Lstat(joined); err != nil {
		return "", fmt.Errorf("path %q not found in source: %w", sub, err)
	}
	if err := safepath.VerifyTree(joined); err != nil {
		return "", fmt.Errorf("source %q cannot be deployed: %w", sub, err)
	}
	return joined, nil
}
