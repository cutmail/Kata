package app

import (
	"context"
	"fmt"

	"github.com/cutmail/kata/internal/lockfile"
	"github.com/cutmail/kata/internal/manifest"
)

// UpdateOptions は update の挙動を制御する。
type UpdateOptions struct {
	// Names は対象を絞る。空なら宣言された全件。
	Names []string
	// DryRun が真なら lock を書かず、動くコミットだけを返す。
	// ref の解決には取得が必要なため、dry-run でもネットワークは使う。
	DryRun bool
	// NoSync が真なら lock だけ更新し、配置は据え置く。
	NoSync bool
}

// UpdateChange は 1 パッケージの更新結果。
type UpdateChange struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Ref  string `json:"ref,omitempty"`
	// From は更新前のコミット。lock に無ければ空。
	From string `json:"from,omitempty"`
	// To は再解決したコミット。
	To     string `json:"to,omitempty"`
	Action Action `json:"action"`
	// Reason は対象にしなかった理由。
	Reason string `json:"reason,omitempty"`
	Err    error  `json:"-"`
}

// UpdateReport は update 全体の結果。
type UpdateReport struct {
	Changes []UpdateChange `json:"changes"`
	DryRun  bool           `json:"dry_run"`
	// Sync は更新後に走らせた同期の結果。走らせなかったときは nil。
	Sync *SyncReport `json:"-"`
}

// Counts は操作ごとの件数を返す。
func (r *UpdateReport) Counts() map[Action]int {
	m := map[Action]int{}
	for _, c := range r.Changes {
		m[c.Action]++
	}
	return m
}

// Failed は失敗した件数を返す。
func (r *UpdateReport) Failed() int { return r.Counts()[ActionFailed] }

// Update は lock を無視して ref から取得元を再解決し、lock を更新する。
//
// lock を書き換えてよいのは add とこの Update だけ。sync が勝手に上流へ
// 追随しないという不変条件は、更新の入口をここに限ることで守られている。
func (a *App) Update(ctx context.Context, opts UpdateOptions) (*UpdateReport, error) {
	targets, err := a.updateTargets(opts.Names)
	if err != nil {
		return nil, err
	}
	rep := &UpdateReport{DryRun: opts.DryRun}

	for _, p := range targets {
		rep.Changes = append(rep.Changes, a.updateOne(ctx, p))
	}

	if opts.DryRun {
		// lock も配置も書き換えない。取得したものが store に残ることはあるが、
		// store は純粋なキャッシュなので実害はない。
		return rep, rep.err()
	}

	if opts.NoSync {
		if err := a.persist(); err != nil {
			return rep, err
		}
		return rep, rep.err()
	}

	// 既定で配置も合わせる。lock だけ進めて放置すると、配置が古い取得物を
	// 指したまま残り、list が drifted を報告し続ける中途半端な状態になる。
	// Sync は更新済みの lock を読むので、取得はキャッシュに当たる。
	sync, serr := a.Sync(ctx, SyncOptions{})
	rep.Sync = sync
	if err := rep.err(); err != nil {
		return rep, err
	}
	return rep, serr
}

// updateTargets は更新対象のパッケージを返す。
func (a *App) updateTargets(names []string) ([]manifest.Package, error) {
	if len(names) == 0 {
		return a.man.Packages, nil
	}
	var out []manifest.Package
	for _, name := range names {
		p, ok := a.man.Find(name)
		if !ok {
			return nil, fmt.Errorf("package %q is not declared in %s", name, a.cfg.ManifestPath)
		}
		out = append(out, p)
	}
	return out, nil
}

// updateOne は 1 パッケージの取得元を ref から解決し直す。
func (a *App) updateOne(ctx context.Context, p manifest.Package) UpdateChange {
	c := UpdateChange{Name: p.Name, Type: p.Type, Ref: p.Ref}

	if p.IsLocal() {
		// local の実体はマニフェストと同じリポジトリにあり、固定すべきコミットを持たない。
		c.Action = ActionUnchanged
		c.Reason = "local package has no pinned commit"
		return c
	}
	if locked, ok := a.lock.Get(p.Name); ok {
		// 取得元によってピンの形が違う。git はコミット、url は内容ダイジェスト。
		c.From = pinOf(locked.Commit, locked.Digest)
	}

	// lock を使わずに取り直させる。ここが update の本体で、sync との唯一の違い。
	fetched, err := a.fetch(ctx, p, false)
	if err != nil {
		c.Action, c.Err = ActionFailed, err
		return c
	}
	c.To = pinOf(fetched.Commit, fetched.Digest)

	a.lock.Put(lockfile.Entry{
		Name:    p.Name,
		Type:    p.Type,
		Source:  p.SourceID(),
		Ref:     p.Ref,
		Commit:  fetched.Commit,
		Subpath: p.Path,
		Local:   p.Local,
		Digest:  fetched.Digest,
	})

	if c.From == c.To {
		c.Action = ActionUnchanged
		return c
	}
	c.Action = ActionUpdate
	return c
}

// pinOf は取得結果のうち、その取得元でピンの役割を果たす値を返す。
func pinOf(commit, dgst string) string {
	if commit != "" {
		return commit
	}
	return dgst
}

// err は失敗があればまとめたエラーを返す。
func (r *UpdateReport) err() error {
	if n := r.Failed(); n > 0 {
		return fmt.Errorf("%d package(s) failed to update", n)
	}
	return nil
}
