package app

import (
	"context"
	"os"

	"github.com/cutmail/kata/internal/digest"
	"github.com/cutmail/kata/internal/linker"
	"github.com/cutmail/kata/internal/manifest"
)

// Status は 1 パッケージの現在の状態。
type Status string

const (
	// StatusLinked は宣言どおりに配置されている状態。
	StatusLinked Status = "linked"
	// StatusMissing は未配置の状態。
	StatusMissing Status = "missing"
	// StatusDrifted は配置先が kata の記録と食い違う状態。
	StatusDrifted Status = "drifted"
	// StatusBroken はリンク先が失われている状態。
	StatusBroken Status = "broken"
	// StatusOrphan は宣言から消えたのに配置が残っている状態。
	StatusOrphan Status = "orphan"
	// StatusCopied は copy 戦略で宣言どおりに配置されている状態。
	StatusCopied Status = "copied"
)

// Deployed は宣言どおりに配置されている状態かを返す。
// status の終了コードと doctor の重み付けが同じ判断を使うように、
// 「健全とみなす状態」の定義をここ 1 箇所に置く。
func (s Status) Deployed() bool { return s == StatusLinked || s == StatusCopied }

// AllStatuses は表示順を固定するための一覧。
var AllStatuses = []Status{
	StatusLinked, StatusCopied, StatusMissing, StatusDrifted, StatusBroken, StatusOrphan,
}

// Item は list の 1 行。
type Item struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Source string `json:"source,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Commit string `json:"commit,omitempty"`
	Dest   string `json:"dest"`
	Status Status `json:"status"`
	// Scope は配置スコープ。配置先が user か project かを見分けるために持つ。
	Scope string `json:"scope,omitempty"`
	// Profiles は所属プロファイル。絞り込みで外れているものを見つけられるよう、
	// list は常に全件を表示してこの列で区別する。
	Profiles []string `json:"profiles,omitempty"`
}

// List は宣言と実配置を突き合わせた一覧を返す。
func (a *App) List(_ context.Context) ([]Item, error) {
	origin := a.Origin()
	items := make([]Item, 0, len(a.man.Packages))
	declared := map[string]bool{}

	for _, p := range a.man.Packages {
		declared[p.Name] = true
		it := Item{
			Name: p.Name, Type: p.Type, Source: p.SourceID(), Ref: p.Ref,
			Scope: p.Scope, Profiles: p.Profiles,
		}
		if e, ok := a.lock.Get(p.Name); ok {
			it.Commit = e.Commit
		}
		dest, err := a.resolver.Resolve(p)
		if err != nil {
			return nil, err
		}
		it.Dest = dest
		it.Status = a.statusOf(origin, p.Name, dest)
		items = append(items, it)
	}

	// 宣言から消えたのに配置実績が残っているものを孤児として報告する。
	for _, e := range a.st.ByOrigin(origin) {
		if declared[e.Name] {
			continue
		}
		items = append(items, Item{
			Name: e.Name, Type: e.Type, Dest: e.Dest, Status: StatusOrphan,
		})
	}
	return items, nil
}

// statusOf は配置先の実際の様子から状態を判定する。
func (a *App) statusOf(origin, name, dest string) Status {
	prev, tracked := a.st.Get(origin, name)
	fi, err := os.Lstat(dest)
	if err != nil {
		return StatusMissing
	}
	// copy 戦略では実体があるのが正解なので、symlink 前提の判定は通せない。
	// 判定の根拠は記録に残したダイジェストだけ。
	if tracked && prev.Strategy == manifest.StrategyCopy {
		if fi.Mode()&os.ModeSymlink != 0 {
			return StatusDrifted
		}
		actual, derr := digest.Tree(dest)
		if derr != nil || !digest.Equal(actual, prev.Digest) {
			return StatusDrifted
		}
		return StatusCopied
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return StatusDrifted
	}
	if linker.IsBrokenLink(dest) {
		return StatusBroken
	}
	if tracked && !linkPointsTo(dest, prev.Target) {
		return StatusDrifted
	}
	if !tracked {
		return StatusDrifted
	}
	return StatusLinked
}
