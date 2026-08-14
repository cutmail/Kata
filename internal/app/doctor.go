package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cutmail/kata/internal/linker"
	"github.com/cutmail/kata/internal/manifest"
)

// CheckLevel は診断結果の重さ。
type CheckLevel string

const (
	// LevelOK は問題が見つからなかったことを示す。
	LevelOK CheckLevel = "ok"
	// LevelWarn は動くが直したほうがよいことを示す。
	LevelWarn CheckLevel = "warn"
	// LevelError はこのままでは正しく動かないことを示す。
	LevelError CheckLevel = "error"
)

// Check は診断項目 1 件の結果。
type Check struct {
	// Name は項目の識別子。出力の目印として安定させる。
	Name  string     `json:"name"`
	Level CheckLevel `json:"level"`
	// Detail は何が分かったか。
	Detail string `json:"detail"`
	// Hint は直し方。無ければ空。
	Hint string `json:"hint,omitempty"`
}

// DoctorReport は診断全体の結果。
type DoctorReport struct {
	Checks []Check `json:"checks"`
}

func (r *DoctorReport) add(c Check) { r.Checks = append(r.Checks, c) }

// Counts はレベルごとの件数を返す。
func (r *DoctorReport) Counts() map[CheckLevel]int {
	m := map[CheckLevel]int{}
	for _, c := range r.Checks {
		m[c.Level]++
	}
	return m
}

// Worst は最も重いレベルを返す。終了コードはこれで決まる。
func (r *DoctorReport) Worst() CheckLevel {
	worst := LevelOK
	for _, c := range r.Checks {
		if c.Level == LevelError {
			return LevelError
		}
		if c.Level == LevelWarn {
			worst = LevelWarn
		}
	}
	return worst
}

// DefaultConfigLoose はマニフェストが見つからなくてもエラーにしない設定を返す。
//
// doctor は環境が壊れているときにこそ必要になる。kata.yml が無いことを
// 実行できない理由ではなく、報告すべき診断結果として扱う。
func DefaultConfigLoose(startDir string) (Config, error) {
	cfg, err := DefaultConfig(startDir)
	if err == nil || !errors.Is(err, ErrManifestNotFound) {
		return cfg, err
	}
	home, herr := os.UserHomeDir()
	if herr != nil {
		return cfg, herr
	}
	return Config{
		KataHome:   envOr("KATA_HOME", filepath.Join(home, ".kata")),
		ClaudeHome: envOr("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude")),
	}, nil
}

// Diagnose は環境を診断する。
//
// ネットワークは使わない。取得元へ届かない状況こそ診断したい場面であり、
// オフラインで最後まで走り切れることに価値がある。
func Diagnose(ctx context.Context, cfg Config) (*DoctorReport, error) {
	rep := &DoctorReport{}

	if cfg.ManifestPath == "" {
		rep.add(Check{
			Name: "manifest", Level: LevelWarn,
			Detail: "no " + manifest.FileName + " found in this directory or its parents",
			Hint:   "run 'kata init' to start one",
		})
		checkSymlinkSupport(rep, cfg.ClaudeHome)
		return rep, nil
	}

	a, err := Open(cfg)
	if err != nil {
		rep.add(Check{
			Name: "manifest", Level: LevelError,
			Detail: fmt.Sprintf("%s cannot be read: %v", Short(cfg.ManifestPath), err),
			Hint:   "fix the syntax; every other command needs this file",
		})
		return rep, nil
	}
	rep.add(Check{
		Name: "manifest", Level: LevelOK,
		Detail: fmt.Sprintf("%s (%d packages)", Short(cfg.ManifestPath), len(a.man.Packages)),
	})

	checkSymlinkSupport(rep, cfg.ClaudeHome)
	a.checkTargets(rep)
	a.checkLockConsistency(rep)
	a.checkDeployment(ctx, rep)
	a.checkStateIntegrity(rep)
	a.checkStore(rep)
	a.checkBackups(rep)
	return rep, nil
}

// checkSymlinkSupport は配置先で symlink を作れるかを見る。
func checkSymlinkSupport(rep *DoctorReport, home string) {
	// 存在しないディレクトリを診断のために作りに行かない。
	if _, err := os.Stat(home); os.IsNotExist(err) {
		rep.add(Check{
			Name: "symlink-support", Level: LevelOK,
			Detail: Short(home) + " does not exist yet; it will be created on the first sync",
		})
		return
	}
	if linker.SymlinkSupported(home) {
		rep.add(Check{
			Name: "symlink-support", Level: LevelOK,
			Detail: Short(home) + " supports symlinks",
		})
		return
	}
	rep.add(Check{
		Name: "symlink-support", Level: LevelError,
		Detail: "cannot create symlinks in " + Short(home),
		Hint:   "declare 'strategy: copy' (or 'auto') for the affected packages",
	})
}

// checkTargets は配置先ディレクトリの存在と書き込み可否を見る。
func (a *App) checkTargets(rep *DoctorReport) {
	seen := map[string]bool{}
	for _, p := range a.man.Packages {
		dir, _, ok := a.resolver.Location(p.Scope, p.Type)
		if !ok || seen[dir] {
			continue
		}
		seen[dir] = true

		if _, err := os.Stat(dir); os.IsNotExist(err) {
			rep.add(Check{
				Name: "target-writable", Level: LevelOK,
				Detail: Short(dir) + " will be created on the first sync",
			})
			continue
		}
		probe, err := os.MkdirTemp(dir, ".kata-probe-")
		if err != nil {
			rep.add(Check{
				Name: "target-writable", Level: LevelError,
				Detail: fmt.Sprintf("%s is not writable: %v", Short(dir), err),
			})
			continue
		}
		_ = os.RemoveAll(probe)
		rep.add(Check{Name: "target-writable", Level: LevelOK, Detail: Short(dir) + " is writable"})
	}
}

// checkLockConsistency は宣言とロックの食い違いを見る。
//
// sync は lock を正として動くため、kata.yml の ref や取得元を書き換えても
// 何も起きない（あるいは分かりにくいエラーで落ちる）。利用者から見て
// 最も気づきにくい種類のずれなので、ここで名指しして直し方を示す。
func (a *App) checkLockConsistency(rep *DoctorReport) {
	declared := map[string]bool{}
	issues := 0

	for _, p := range a.man.Packages {
		declared[p.Name] = true
		locked, ok := a.lock.Get(p.Name)
		if !ok {
			rep.add(Check{
				Name: "lock-consistency", Level: LevelWarn,
				Detail: fmt.Sprintf("%s: declared but not resolved yet", p.Name),
				Hint:   "run 'kata sync'",
			})
			issues++
			continue
		}
		if p.SourceID() != locked.Source {
			// この食い違いは sync が意味の取りにくいエラーで落ちる原因になる。
			rep.add(Check{
				Name: "lock-consistency", Level: LevelWarn,
				Detail: fmt.Sprintf("%s: the manifest source changed since the lock was written (%s -> %s)",
					p.Name, locked.Source, p.SourceID()),
				Hint: "run 'kata update " + p.Name + "'",
			})
			issues++
			continue
		}
		if p.Ref != locked.Ref {
			// sync は lock のコミットを使い続けるので、ref を書き換えても無言で無視される。
			rep.add(Check{
				Name: "lock-consistency", Level: LevelWarn,
				Detail: fmt.Sprintf("%s: the manifest declares ref %q but the lock pins ref %q",
					p.Name, p.Ref, locked.Ref),
				Hint: "run 'kata update " + p.Name + "'",
			})
			issues++
		}
	}
	for _, e := range a.lock.Resolved {
		if !declared[e.Name] {
			rep.add(Check{
				Name: "lock-consistency", Level: LevelWarn,
				Detail: fmt.Sprintf("%s: stale lock entry for a package that is no longer declared", e.Name),
				Hint:   "run 'kata sync'",
			})
			issues++
		}
	}
	if issues == 0 {
		rep.add(Check{
			Name: "lock-consistency", Level: LevelOK,
			Detail: "the manifest and the lock agree",
		})
	}
}

// checkDeployment は宣言と実配置の突き合わせ結果を診断へ写す。
func (a *App) checkDeployment(ctx context.Context, rep *DoctorReport) {
	items, err := a.List(ctx)
	if err != nil {
		rep.add(Check{Name: "deployment", Level: LevelError, Detail: err.Error()})
		return
	}
	issues := 0
	for _, it := range items {
		if it.Status.Deployed() {
			continue
		}
		issues++
		level := LevelWarn
		if it.Status == StatusBroken {
			level = LevelError
		}
		rep.add(Check{
			Name: "deployment", Level: level,
			Detail: fmt.Sprintf("%s: %s (%s)", it.Name, it.Status, Short(it.Dest)),
			Hint:   "run 'kata sync'",
		})
	}
	if issues == 0 {
		rep.add(Check{
			Name: "deployment", Level: LevelOK,
			Detail: fmt.Sprintf("%d package(s) deployed as declared", len(items)),
		})
	}
}

// checkStateIntegrity は配置実績そのものの健全性を見る。
func (a *App) checkStateIntegrity(rep *DoctorReport) {
	// state.json を失うと、リンクが完璧でも list は全件をズレとして報告する。
	// 症状だけ見ると壊れているように見えるので、原因を名指しする。
	if _, err := os.Stat(a.cfg.StatePath()); os.IsNotExist(err) && len(a.man.Packages) > 0 {
		rep.add(Check{
			Name: "state-integrity", Level: LevelWarn,
			Detail: Short(a.cfg.StatePath()) + " is missing, so kata cannot tell which deployments are its own",
			Hint:   "run 'kata sync' to record them again",
		})
		return
	}

	dead := a.deadStateEntries()
	if len(dead) > 0 {
		for _, d := range dead {
			rep.add(Check{
				Name: "state-integrity", Level: LevelWarn,
				Detail: fmt.Sprintf("%s: recorded at %s but nothing is there", d.Name, Short(d.Dest)),
				Hint:   "run 'kata prune --state --apply'",
			})
		}
		return
	}
	rep.add(Check{
		Name: "state-integrity", Level: LevelOK,
		Detail: fmt.Sprintf("%d deployment record(s) look consistent", len(a.st.Entries)),
	})
}

// checkStore はキャッシュの状況を見る。
func (a *App) checkStore(rep *DoctorReport) {
	entries, err := os.ReadDir(a.store.ReposRoot())
	if os.IsNotExist(err) {
		rep.add(Check{Name: "store", Level: LevelOK, Detail: "the cache is empty"})
	} else if err != nil {
		rep.add(Check{Name: "store", Level: LevelWarn, Detail: err.Error()})
	} else {
		live := a.liveStoreKeys()
		orphans := 0
		for _, e := range entries {
			if !live[e.Name()] {
				orphans++
			}
		}
		detail := fmt.Sprintf("%d cached item(s)", len(entries))
		if orphans > 0 {
			detail += fmt.Sprintf(", %d not referenced from here", orphans)
		}
		rep.add(Check{Name: "store", Level: LevelOK, Detail: detail})
	}

	staging, err := os.ReadDir(a.store.StagingRoot())
	if err == nil && len(staging) > 0 {
		rep.add(Check{
			Name: "store", Level: LevelWarn,
			Detail: fmt.Sprintf("%d interrupted fetch(es) left in %s", len(staging), Short(a.store.StagingRoot())),
			Hint:   "run 'kata prune --staging --apply'",
		})
	}
}

// checkBackups は退避物の量を知らせる。
// kata は退避物を消さないので、報告するだけで判断は利用者に委ねる。
func (a *App) checkBackups(rep *DoctorReport) {
	entries, err := os.ReadDir(a.cfg.BackupDir())
	if err != nil || len(entries) == 0 {
		return
	}
	rep.add(Check{
		Name: "backups", Level: LevelOK,
		Detail: fmt.Sprintf("%d snapshot(s) (%s) in %s",
			len(entries), humanBytes(dirSize(a.cfg.BackupDir())), Short(a.cfg.BackupDir())),
		Hint: "kata never removes these; delete them yourself when you no longer need them",
	})
}

// humanBytes はバイト数を読みやすい単位にする。
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
