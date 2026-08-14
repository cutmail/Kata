// Package app は各層を束ね、CLI から呼ばれる操作を実装する。
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cutmail/kata/internal/digest"
	"github.com/cutmail/kata/internal/lockfile"
	"github.com/cutmail/kata/internal/manifest"
	"github.com/cutmail/kata/internal/source"
	"github.com/cutmail/kata/internal/state"
	"github.com/cutmail/kata/internal/store"
	"github.com/cutmail/kata/internal/target"
)

// Config は実行時のパス設定。テストから差し替えられるよう明示的に持つ。
type Config struct {
	// ManifestPath は kata.yml の絶対パス。
	ManifestPath string
	// KataHome はキャッシュと状態の置き場（通常は ~/.kata）。
	KataHome string
	// ClaudeHome は配置先（通常は ~/.claude）。
	ClaudeHome string
}

// App は 1 回の操作に必要な状態をまとめて保持する。
type App struct {
	cfg      Config
	man      *manifest.Manifest
	lock     *lockfile.Lock
	st       *state.State
	store    *store.Store
	git      *source.Git
	url      *source.URL
	local    *source.Local
	resolver target.Resolver
}

// LockPath はロックファイルの絶対パスを返す。
func (c Config) LockPath() string {
	return filepath.Join(filepath.Dir(c.ManifestPath), lockfile.FileName)
}

// StatePath は配置実績ファイルの絶対パスを返す。
func (c Config) StatePath() string { return filepath.Join(c.KataHome, "state.json") }

// StoreRoot はキャッシュ領域のルートを返す。
func (c Config) StoreRoot() string { return filepath.Join(c.KataHome, "store") }

// BackupDir は退避先を返す。
func (c Config) BackupDir() string { return filepath.Join(c.KataHome, "backups") }

// ProjectHome は project スコープの配置先を返す。
//
// kata.yml と同じ階層の .claude を使う。フィールドではなく導出にしているのは、
// マニフェストは上位ディレクトリへ遡って探されるため、
// 「マニフェストのある場所＝プロジェクトルート」が唯一の正しい定義だから。
// サブディレクトリから sync しても配置先は動かない。
func (c Config) ProjectHome() string {
	return filepath.Join(filepath.Dir(c.ManifestPath), ".claude")
}

// DefaultConfig は環境変数と既定値からパス設定を組み立てる。
// 探索の起点から上位ディレクトリへ向かって kata.yml を探す。
func DefaultConfig(startDir string) (Config, error) {
	var cfg Config

	manifestPath, err := findManifest(startDir)
	if err != nil {
		return cfg, err
	}
	cfg.ManifestPath = manifestPath

	home, err := os.UserHomeDir()
	if err != nil {
		return cfg, err
	}
	cfg.KataHome = envOr("KATA_HOME", filepath.Join(home, ".kata"))
	cfg.ClaudeHome = envOr("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	return cfg, nil
}

// ErrManifestNotFound はマニフェストが見つからないことを示す。
var ErrManifestNotFound = errors.New("kata.yml not found (run 'kata init' first)")

func findManifest(startDir string) (string, error) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, manifest.FileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrManifestNotFound
		}
		dir = parent
	}
}

// Short はホームディレクトリを ~ に畳んで読みやすくする。
// 診断文のように、パスを人が読む文へ埋め込むときに使う。
func Short(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// OpenFrom は startDir から上向きに kata.yml を探し、見つかった設定で App を開く。
//
// CLI はカレントディレクトリを起点に、MCP サーバーは呼び出しごとの dir 引数を起点に
// この関数を呼ぶ。「startDir に cd してコマンドを実行した」のと同じ結果になる。
func OpenFrom(startDir string) (*App, Config, error) {
	cfg, err := DefaultConfig(startDir)
	if err != nil {
		return nil, cfg, err
	}
	a, err := Open(cfg)
	return a, cfg, err
}

// Open は設定に従って各層を初期化する。
func Open(cfg Config) (*App, error) {
	man, err := manifest.Load(cfg.ManifestPath)
	if err != nil {
		return nil, err
	}
	lk, err := lockfile.Load(cfg.LockPath())
	if err != nil {
		return nil, err
	}
	st, err := state.Load(cfg.StatePath())
	if err != nil {
		return nil, err
	}
	s := store.New(cfg.StoreRoot())
	return &App{
		cfg:      cfg,
		man:      man,
		lock:     lk,
		st:       st,
		store:    s,
		git:      source.NewGit(s),
		url:      source.NewURL(s),
		local:    source.NewLocal(),
		resolver: target.NewClaudeCode(cfg.ClaudeHome, cfg.ProjectHome()),
	}, nil
}

// Manifest は読み込み済みのマニフェストを返す。
func (a *App) Manifest() *manifest.Manifest { return a.man }

// Origin はこのマニフェストを識別するディレクトリを返す。
func (a *App) Origin() string { return a.man.Dir() }

// Init は指定ディレクトリにマニフェストの雛形と local/ を作る。
func Init(dir string) (string, error) {
	path := filepath.Join(dir, manifest.FileName)
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s already exists", manifest.FileName)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}

	for _, sub := range []string{"local/skills", "local/commands", "local/agents"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(path, []byte(template), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

const template = `version: 1

defaults:
  scope: user      # user（~/.claude）または project（このリポジトリの .claude）
  strategy: link   # link / copy / auto（symlink が使えなければ copy）

# 取得元をまとめて定義しておくと packages から参照できる
sources: {}
#  anthropic:
#    git: https://github.com/anthropics/skills
#    ref: main

packages: []
#  - name: pdf
#    type: skill            # skill / command / agent
#    from: anthropic
#    path: skills/pdf
#
#  - name: my-review
#    type: skill
#    local: ./local/skills/my-review
#    profiles: [work]       # kata sync --profile work のときだけ配置する
#
#  - name: toolkit
#    type: skill
#    url: https://example.com/toolkit-1.4.0.tar.gz
#    sha256: 9f86d081...    # 任意。書かなければ初回取得時に kata.lock へ記録される
#    path: toolkit-1.4.0/skills/toolkit
`

// fetch はパッケージ種別に応じた取得元へ処理を振り分ける。
//
// useLock が真なら lock に記録済みの解決結果を使う（sync の経路）。
// 偽なら宣言だけを見て取り直す（update の経路）。
// 取得元によってピンの形が違う（git は commit、url はダイジェスト）ので、
// 「lock を使うか」は値の有無から推測せず、呼び出し側が明示する。
func (a *App) fetch(ctx context.Context, p manifest.Package, useLock bool) (source.Fetched, error) {
	req := source.Request{
		Git:      p.Git,
		Ref:      p.Ref,
		Path:     p.Path,
		Local:    p.Local,
		URL:      p.URL,
		LocalGit: p.IsLocalGit(),
		BaseDir:  a.man.Dir(),
	}
	if useLock {
		if e, ok := a.lock.Get(p.Name); ok {
			req.Commit = e.Commit
		}
	}
	switch {
	case p.IsLocal():
		return a.local.Fetch(ctx, req)
	case p.IsURL():
		want, err := a.expectedDigest(p, useLock)
		if err != nil {
			return source.Fetched{}, err
		}
		req.Digest = want
		return a.url.Fetch(ctx, req)
	}
	return a.git.Fetch(ctx, req)
}

// expectedDigest は url 取得物に期待するダイジェストを決める。
//
// url には commit に相当するものが無いため、内容ダイジェストが唯一のピンになる。
// マニフェストの sha256 と lock の記録が食い違うときは、どちらかを黙って
// 採用せずに止める。ダイジェストは「浮動する参照」ではなく「完全性の主張」であり、
// 利用者が書き換えた主張を無視するのは危険だから。
// ref の変更を lock が黙って上書きするのとは、意図的に非対称にしてある。
//
// useLock が偽なら update からの呼び出しで、lock を無視して取り直す。
func (a *App) expectedDigest(p manifest.Package, useLock bool) (string, error) {
	declared := ""
	if p.SHA256 != "" {
		declared = digest.Prefix + p.SHA256
	}
	if !useLock {
		return declared, nil
	}
	locked := ""
	if e, ok := a.lock.Get(p.Name); ok {
		locked = e.Digest
	}
	if declared != "" && locked != "" && declared != locked {
		return "", fmt.Errorf(
			"package %q: the manifest declares %s but the lock pins %s; "+
				"run 'kata update %s' to accept the manifest value",
			p.Name, declared, locked, p.Name)
	}
	if locked != "" {
		return locked, nil
	}
	return declared, nil
}
