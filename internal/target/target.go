// Package target は「パッケージ種別 → 配置先パス」の解決を担う。
// 他のエージェントに対応する際は、この Resolver を足すだけで済む。
package target

import (
	"fmt"
	"path/filepath"

	"github.com/cutmail/kata/internal/manifest"
	"github.com/cutmail/kata/internal/safepath"
)

// Resolver は配置先を決める。
type Resolver interface {
	// Name は解決器の識別名。
	Name() string
	// Supports はその種別を扱えるかを返す。
	Supports(pkgType string) bool
	// Resolve は配置先の絶対パスを返す。
	Resolve(p manifest.Package) (string, error)
	// ExpectDir は配置元がディレクトリであるべきかを返す。
	ExpectDir(pkgType string) bool
	// Types は扱える種別を、走査したい順に返す。
	Types() []string
	// Location は種別に対応する配置先ディレクトリと、実体に付く拡張子を返す。
	// import が Resolve と同じ規則で既存の配置を走査するために持つ。
	// ext はディレクトリ種別なら空文字。
	Location(scope, pkgType string) (dir, ext string, ok bool)
}

// ClaudeCode は Claude Code の設定ディレクトリへ配置する解決器。
type ClaudeCode struct {
	// Home は user スコープの配置先（通常は ~/.claude）。
	Home string
	// ProjectHome は project スコープの配置先（kata.yml と同じ階層の .claude）。
	ProjectHome string
}

// placement は 1 種別の配置先の形。
type placement struct {
	dir string
	// ext が空なら実体はディレクトリ。
	ext string
}

// layout は種別ごとの配置先の形。
// Resolve も Location も同じ表を引くことで、配置と走査の規則がずれないようにする。
var layout = map[string]placement{
	manifest.TypeSkill:   {dir: "skills"},
	manifest.TypeCommand: {dir: "commands", ext: ".md"},
	manifest.TypeAgent:   {dir: "agents", ext: ".md"},
}

// typeOrder は走査する順。map の反復順は不定なので明示的に持つ。
var typeOrder = []string{manifest.TypeSkill, manifest.TypeCommand, manifest.TypeAgent}

// NewClaudeCode は解決器を返す。
func NewClaudeCode(home, projectHome string) *ClaudeCode {
	return &ClaudeCode{Home: home, ProjectHome: projectHome}
}

// Name は解決器の識別名を返す。
func (c *ClaudeCode) Name() string { return "claude-code" }

// Supports は扱える種別かを返す。
func (c *ClaudeCode) Supports(pkgType string) bool {
	_, ok := layout[pkgType]
	return ok
}

// ExpectDir は skill だけがディレクトリで、command と agent は単一ファイルであることを示す。
func (c *ClaudeCode) ExpectDir(pkgType string) bool {
	l, ok := layout[pkgType]
	return ok && l.ext == ""
}

// Types は扱える種別を、走査したい順に返す。
func (c *ClaudeCode) Types() []string { return append([]string(nil), typeOrder...) }

// Location は種別に対応する配置先ディレクトリと拡張子を返す。
func (c *ClaudeCode) Location(scope, pkgType string) (dir, ext string, ok bool) {
	root, err := c.root(scope)
	if err != nil {
		return "", "", false
	}
	l, ok := layout[pkgType]
	if !ok {
		return "", "", false
	}
	return filepath.Join(root, l.dir), l.ext, true
}

// Resolve は種別に応じた配置先を返す。
//
// 配置先を組み立てるだけでなく、そこへ至る経路に symlink が無いことまで確かめる。
// filepath.Join は純粋に字句的なので、リポジトリにコミットされた .claude の
// symlink 1 本で、内側に見えるパスがリポジトリの外を指しうる。
func (c *ClaudeCode) Resolve(p manifest.Package) (string, error) {
	root, err := c.root(p.Scope)
	if err != nil {
		return "", fmt.Errorf("package %q: %w", p.Name, err)
	}
	l, ok := layout[p.Type]
	if !ok {
		return "", fmt.Errorf("package %q: unsupported type %q", p.Name, p.Type)
	}
	dest := filepath.Join(root, l.dir, p.Name+l.ext)

	// 検査の起点はスコープによって変える。
	//
	// user スコープの root は利用者が自分で決めた場所なので、そこが symlink でも
	// 利用者の意思として尊重する。一方 project スコープの root はマニフェストと
	// 同じリポジトリにある .claude であり、clone してきたリポジトリに
	// コミットされた symlink でありうる。そちらは 1 つ上から検査して、
	// .claude 自身も確かめる。
	base := root
	if p.Scope == manifest.ScopeProject {
		base = filepath.Dir(root)
	}
	// 末端は kata 自身が symlink にしうるので、その親までを検査する。
	if err := safepath.VerifyUnder(base, filepath.Dir(dest)); err != nil {
		return "", fmt.Errorf("package %q: refusing to deploy into %s: %w", p.Name, dest, err)
	}
	return dest, nil
}

// root はスコープに対応する配置先のルートを返す。
// スコープが変わってもディレクトリ構成は同じで、根だけが変わる。
func (c *ClaudeCode) root(scope string) (string, error) {
	switch scope {
	case manifest.ScopeUser:
		return c.Home, nil
	case manifest.ScopeProject:
		if c.ProjectHome == "" {
			return "", fmt.Errorf("scope %q requires a project directory", scope)
		}
		return c.ProjectHome, nil
	}
	return "", fmt.Errorf("unsupported scope %q", scope)
}
