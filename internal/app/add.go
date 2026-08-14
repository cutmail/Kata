package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cutmail/kata/internal/manifest"
)

// AddSpec は kata add に渡される指定。
type AddSpec struct {
	// Source は取得元。git の URL 短縮形かローカルパス。
	Source string
	Name   string
	Type   string
	Path   string
	Ref    string
	// Scope は配置スコープ。空なら user。
	Scope string
	// Strategy は配置戦略。空なら link。
	Strategy string
	// IsURL が真なら、取得元を書庫として扱う。拡張子で判断がつかないときに使う。
	IsURL bool
	// Profiles は所属プロファイル。空なら常に選択される。
	Profiles []string
}

// Add はマニフェストにパッケージを追記する。
// 追記後の配置は呼び出し側が Sync を実行して行う。
func (a *App) Add(_ context.Context, spec AddSpec) (manifest.Package, error) {
	p, err := a.buildPackage(spec)
	if err != nil {
		return manifest.Package{}, err
	}
	if err := a.man.Add(p); err != nil {
		return manifest.Package{}, err
	}
	if err := a.man.Validate(); err != nil {
		a.man.Remove(p.Name)
		return manifest.Package{}, err
	}
	if err := a.man.Save(a.cfg.ManifestPath); err != nil {
		return manifest.Package{}, err
	}
	return p, nil
}

// buildPackage は指定を解釈して 1 件のパッケージ定義に組み立てる。
func (a *App) buildPackage(spec AddSpec) (manifest.Package, error) {
	scope := spec.Scope
	if scope == "" {
		scope = manifest.ScopeUser
	}
	strategy := spec.Strategy
	if strategy == "" {
		strategy = manifest.StrategyLink
	}
	p := manifest.Package{
		Name:     spec.Name,
		Type:     spec.Type,
		Path:     spec.Path,
		Ref:      spec.Ref,
		Scope:    scope,
		Strategy: strategy,
		Profiles: spec.Profiles,
	}

	switch {
	case spec.IsURL || isArchiveURL(spec.Source):
		// 書庫の URL は git の URL と見分けがつかないので、拡張子で判断する。
		// ここを通さないと https://example.com/x.tar.gz を git リポジトリとして
		// clone しようとして、分かりにくいエラーになる。
		p.URL = spec.Source
		base := spec.Path
		if base == "" {
			base = strings.TrimSuffix(strings.TrimSuffix(baseName(spec.Source), ".gz"), ".tar")
			base = strings.TrimSuffix(base, ".tgz")
			base = strings.TrimSuffix(base, ".zip")
		}
		if p.Name == "" {
			p.Name = defaultName(base)
		}
		if p.Type == "" {
			p.Type = guessTypeByName(base)
		}
	case isLocalPath(spec.Source):
		rel, abs, err := a.localRel(spec.Source)
		if err != nil {
			return p, err
		}
		if spec.Path != "" {
			return p, fmt.Errorf("--path cannot be combined with a local source")
		}
		p.Local = rel
		if p.Name == "" {
			p.Name = defaultName(abs)
		}
		if p.Type == "" {
			p.Type = guessType(abs)
		}
	default:
		url, err := normalizeGitURL(spec.Source)
		if err != nil {
			return p, err
		}
		p.Git = url
		base := spec.Path
		if base == "" {
			base = strings.TrimSuffix(baseName(url), ".git")
		}
		if p.Name == "" {
			p.Name = defaultName(base)
		}
		if p.Type == "" {
			p.Type = guessTypeByName(base)
		}
	}

	if p.Type == "" {
		return p, fmt.Errorf("could not determine the package type; pass --type skill or --type command")
	}
	return p, nil
}

// localRelFor は絶対パスをマニフェスト相対の local: 値に変換する。
//
// 実在確認をしないのは、import の dry-run が「これから作るパス」を示すときに、
// 本番で実際に書き込む値と同じ文字列を得られるようにするため。
// dry-run の表示と実際の書き込みは、必ずここから得た同じ値を使う。
func (a *App) localRelFor(abs string) (string, error) {
	rel, err := filepath.Rel(a.man.Dir(), abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("local source must live inside the manifest directory (%s)", a.man.Dir())
	}
	return "./" + filepath.ToSlash(rel), nil
}

// localRel はローカル指定を実在確認つきでマニフェスト相対のパスに変換する。
func (a *App) localRel(src string) (rel, abs string, err error) {
	abs, err = filepath.Abs(src)
	if err != nil {
		return "", "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", "", fmt.Errorf("local source %q not found: %w", src, err)
	}
	rel, err = a.localRelFor(abs)
	if err != nil {
		return "", "", err
	}
	return rel, abs, nil
}

// isArchiveURL は指定が書庫の取得先かを判定する。
//
// git の URL と形が同じなので、拡張子でしか見分けられない。
// 判断がつかない場合のために --url も用意してある。
func isArchiveURL(src string) bool {
	if !strings.Contains(src, "://") {
		return false
	}
	lower := strings.ToLower(src)
	// クエリや断片が付いていても拡張子を見られるようにする。
	if i := strings.IndexAny(lower, "?#"); i >= 0 {
		lower = lower[:i]
	}
	for _, ext := range []string{".tar.gz", ".tgz", ".zip", ".tar"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// isLocalPath は指定がローカルパスかを判定する。
func isLocalPath(src string) bool {
	if src == "" {
		return false
	}
	if strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") || strings.HasPrefix(src, "/") || src == "." {
		return true
	}
	if strings.Contains(src, "://") {
		return false
	}
	// 実在するパスならローカル扱いにする。
	if _, err := os.Stat(src); err == nil {
		return true
	}
	return false
}

// normalizeGitURL は owner/repo などの短縮形を URL に展開する。
func normalizeGitURL(src string) (string, error) {
	switch {
	case src == "":
		return "", fmt.Errorf("source is required")
	case strings.Contains(src, "://"), strings.HasPrefix(src, "git@"):
		return src, nil
	case strings.HasPrefix(src, "github.com/"):
		return "https://" + src, nil
	}
	if strings.Count(src, "/") == 1 {
		return "https://github.com/" + src, nil
	}
	return "", fmt.Errorf("cannot interpret %q as a git source; pass a full URL", src)
}

// defaultName はパスからパッケージ名を導く。
func defaultName(p string) string {
	base := baseName(p)
	base = strings.TrimSuffix(base, ".md")
	base = strings.TrimSuffix(base, ".git")
	return strings.ToLower(base)
}

// guessType はローカル実体の形から種別を推測する。
func guessType(abs string) string {
	fi, err := os.Stat(abs)
	if err != nil {
		return ""
	}
	if fi.IsDir() {
		return manifest.TypeSkill
	}
	if strings.HasSuffix(strings.ToLower(abs), ".md") {
		return manifest.TypeCommand
	}
	return ""
}

// guessTypeByName はパス表記だけから種別を推測する。
func guessTypeByName(p string) string {
	if strings.HasSuffix(strings.ToLower(p), ".md") {
		return manifest.TypeCommand
	}
	return manifest.TypeSkill
}

// baseName はスラッシュ区切りにも対応した basename を返す。
func baseName(p string) string {
	p = strings.TrimSuffix(strings.ReplaceAll(p, "\\", "/"), "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
