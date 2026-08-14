package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/cutmail/kata/internal/digest"
	"github.com/cutmail/kata/internal/linker"
	"github.com/cutmail/kata/internal/lockfile"
	"github.com/cutmail/kata/internal/manifest"
	"github.com/cutmail/kata/internal/source"
	"github.com/cutmail/kata/internal/state"
)

// SyncOptions は sync の挙動を制御する。
type SyncOptions struct {
	// DryRun が真なら取得も配置も行わず、予定だけを返す。
	DryRun bool
	// Force が真なら配置先の既存実体を退避してから配置する。
	Force bool
	// Profile は配置する対象を絞る。空ならすべてが対象。
	Profile string
	// Prune が真なら、Profile で外れたパッケージの配置を撤去する。
	// 宣言には残っているので、lock のピンは残す。
	Prune bool
	// Adopt が真なら、copy 戦略で配置先の内容が置くべき内容と完全に同じ場合に、
	// ファイルには触れないまま管理下として引き取る。
	Adopt bool

	// stamp は退避先のサブディレクトリ名。Sync が実行ごとに一度だけ決める。
	// 1 回の実行でどけたものが 1 つのディレクトリに揃うようにするため。
	stamp string
}

// Action は 1 パッケージに対して行われた（行われる）操作。
type Action string

const (
	ActionCreate    Action = "create"
	ActionUpdate    Action = "update"
	ActionRemove    Action = "remove"
	ActionUnchanged Action = "unchanged"
	ActionFailed    Action = "failed"
)

// Change は sync の結果 1 件。
type Change struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Action  Action `json:"action"`
	Dest    string `json:"dest,omitempty"`
	Target  string `json:"target,omitempty"`
	Warning string `json:"warning,omitempty"`
	Err     error  `json:"-"`
}

// SyncReport は sync 全体の結果。
type SyncReport struct {
	Changes []Change `json:"changes"`
	DryRun  bool     `json:"dry_run"`
}

// Failed は失敗した件数を返す。
func (r *SyncReport) Failed() int {
	n := 0
	for _, c := range r.Changes {
		if c.Err != nil {
			n++
		}
	}
	return n
}

// Counts は操作ごとの件数を返す。
func (r *SyncReport) Counts() map[Action]int {
	m := map[Action]int{}
	for _, c := range r.Changes {
		m[c.Action]++
	}
	return m
}

// Sync は宣言された状態に実配置を一致させる。冪等に実行できる。
func (a *App) Sync(ctx context.Context, opts SyncOptions) (*SyncReport, error) {
	// dry-run は何も書かないので、他の kata を待たせる理由がない。
	if opts.DryRun {
		return a.sync(ctx, opts)
	}
	var rep *SyncReport
	var syncErr error
	if err := a.mutate(func() error {
		rep, syncErr = a.sync(ctx, opts)
		return nil
	}); err != nil {
		return nil, err
	}
	return rep, syncErr
}

func (a *App) sync(ctx context.Context, opts SyncOptions) (*SyncReport, error) {
	origin := a.Origin()
	rep := &SyncReport{DryRun: opts.DryRun}

	// 退避先の名前は実行ごとに一度だけ決める。呼び出しのたびに時刻から作ると、
	// 同じ実行の退避物が秒をまたいで別のディレクトリに散ってしまう。
	if opts.stamp == "" {
		opts.stamp = linker.NewStamp()
	}

	// 知らない profile を「該当なし」として黙って受け入れると、何も起きないまま
	// 成功して返る。打ち間違いに気づけないので、変更を始める前に弾く。
	if opts.Profile != "" && !slices.Contains(a.man.Profiles(), opts.Profile) {
		return nil, fmt.Errorf("unknown profile %q (declared: %s)",
			opts.Profile, strings.Join(a.man.Profiles(), ", "))
	}

	// declared は宣言された全パッケージ、selected は今回配置する対象。
	//
	// 撤去と lock の掃除は必ず declared で判定する。ここに selected を渡すと、
	// profile で選ばれなかっただけのパッケージまで配置が剥がされ、さらに lock から
	// コミットのピンが消える。ピンを失うと別マシンで同じツリーを復元できなくなり、
	// しかもその症状は別マシンでしか現れない。
	declared := map[string]bool{}
	selected := map[string]bool{}

	for _, p := range a.man.Packages {
		declared[p.Name] = true
		if !p.HasProfile(opts.Profile) {
			continue
		}
		selected[p.Name] = true
		rep.Changes = append(rep.Changes, a.syncOne(ctx, p, origin, opts))
	}

	// 配置実績のうち、今回配置し直さなかったものを見ていく。
	for _, e := range a.st.ByOrigin(origin) {
		switch {
		case selected[e.Name]:
			// 今回配置し直した。
		case !declared[e.Name]:
			// 宣言から消えた。撤去し、ピンも落とす。
			rep.Changes = append(rep.Changes, a.undeployOne(e, origin, opts, true))
		case opts.Prune:
			// profile で外れただけ。--prune のときだけ剥がすが、宣言には残って
			// いるのでピンは残す。
			rep.Changes = append(rep.Changes, a.undeployOne(e, origin, opts, false))
		default:
			// 残置を知らせる。黙って残ると、なぜ配置されているのか分からなくなる。
			rep.Changes = append(rep.Changes, Change{
				Name: e.Name, Type: e.Type, Action: ActionUnchanged, Dest: e.Dest, Target: e.Target,
				Warning: fmt.Sprintf("deployed but not selected by profile %q", opts.Profile),
			})
		}
	}

	if !opts.DryRun {
		// 宣言に無いロックエントリも掃除する。declared で判定するのは、
		// profile で外れただけのパッケージのピンを失わないため。
		for _, e := range append([]lockfile.Entry(nil), a.lock.Resolved...) {
			if !declared[e.Name] {
				a.lock.Delete(e.Name)
			}
		}
		if err := a.persist(); err != nil {
			return rep, err
		}
	}

	if n := rep.Failed(); n > 0 {
		return rep, fmt.Errorf("%d package(s) failed", n)
	}
	return rep, nil
}

// syncOne は 1 パッケージを取得して配置する。
func (a *App) syncOne(ctx context.Context, p manifest.Package, origin string, opts SyncOptions) Change {
	c := Change{Name: p.Name, Type: p.Type}

	dest, err := a.resolver.Resolve(p)
	if err != nil {
		c.Action, c.Err = ActionFailed, err
		return c
	}
	c.Dest = dest

	if opts.DryRun {
		c.Action = a.planAction(origin, p.Name, dest)
		return c
	}

	// 配置し直す前の記録を捕まえておく。deploy が記録を書き替えてしまうため、
	// 配置先が動いたかどうかはここで見ておかないと分からなくなる。
	prev, hadPrev := a.st.Get(origin, p.Name)

	fetched, err := a.fetch(ctx, p, true)
	if err != nil {
		c.Action, c.Err = ActionFailed, err
		return c
	}
	c.Target = fetched.Root

	warning, err := a.checkShape(p, fetched.Root)
	if err != nil {
		c.Action, c.Err = ActionFailed, err
		return c
	}
	c.Warning = warning

	res, err := a.deploy(p, dest, fetched, a.linkOptions(opts), origin)
	if err != nil {
		c.Action, c.Err = ActionFailed, err
		return c
	}
	if res == linker.Adopted {
		// 引き取りは「手で置いたものが kata のものになる」操作なので、
		// 結果が unchanged でも黙って済ませない。
		c.addWarning("adopted: kata now owns " + dest)
	}

	// scope や type を変えると配置先が動く。古い配置をここで撤去しないと、
	// 記録は新しい場所に上書きされ、古い方は誰にも撤去できない孤児として残る。
	// 新しい配置が成功してから消すことで、途中で失敗しても何も無い状態を作らない。
	if hadPrev && prev.Dest != dest {
		if _, rerr := a.undeploy(prev); rerr != nil {
			c.addWarning(fmt.Sprintf("could not remove the previous deployment at %s: %v", prev.Dest, rerr))
		} else {
			c.addWarning("moved from " + prev.Dest)
		}
	}
	c.Action = actionFor(res)
	return c
}

// undeployOne は記録された配置を剥がし、報告 1 件を作る。
//
// dropLock は lock からもピンを落とすかどうか。宣言から消えたものは落とすが、
// profile で外れただけのものは宣言に残っているので落としてはならない。
func (a *App) undeployOne(e state.Entry, origin string, opts SyncOptions, dropLock bool) Change {
	c := Change{Name: e.Name, Type: e.Type, Action: ActionRemove, Dest: e.Dest, Target: e.Target}
	if opts.DryRun {
		return c
	}
	removed, err := a.undeploy(e)
	if err != nil {
		c.Action, c.Err = ActionFailed, err
		return c
	}
	if !removed {
		c.Warning = "left in place: not a kata-managed link anymore"
	}
	a.st.Delete(origin, e.Name)
	if dropLock {
		a.lock.Delete(e.Name)
	}
	return c
}

// undeploy は kata が置いた配置を剥がす。
//
// 「kata の配置を剥がす」判定をここ 1 箇所に集約する。撤去ループ・remove・
// 配置先が動いたときの後始末が、必ず同じ規則で動くようにするため。
func (a *App) undeploy(e state.Entry) (bool, error) {
	// 記録された配置先が本当に配置ルートの内側かを、消す直前に確かめる。
	//
	// state.json は平文の JSON で、壊れることも手で編集されることもある。
	// kata 自身が書く Dest は必ず resolver 由来なのでこの検査を素通りするが、
	// そうでない値を渡されたときに削除まで進ませない。証明の層を 1 枚しか
	// 持たないのは、取り返しのつかない操作としては薄すぎる。
	if err := a.verifyDeployRoot(e.Dest); err != nil {
		return false, err
	}
	// どちらの経路で剥がすかは記録が決める。宣言側の戦略を見ると、
	// 宣言を書き換えた直後に、実際に置かれているものと違う判定をしてしまう。
	if e.Strategy == manifest.StrategyCopy {
		return linker.RemoveCopy(e.Dest, e.Digest)
	}
	return linker.Remove(e.Dest, e.Target)
}

// verifyDeployRoot は dest が kata の配置ルートの内側にあることを確かめる。
func (a *App) verifyDeployRoot(dest string) error {
	for _, root := range []string{a.cfg.ClaudeHome, a.cfg.ProjectHome()} {
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, dest)
		if err != nil || rel == "." {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		return nil
	}
	return fmt.Errorf("refusing to touch %s: it is outside every deployment root", dest)
}

// addWarning は警告を積む。1 件の配置で複数の注意が出ることがある。
func (c *Change) addWarning(msg string) {
	if c.Warning == "" {
		c.Warning = msg
		return
	}
	c.Warning += "; " + msg
}

// deploy は取得済みの実体を配置し、lock と state に記録する。
//
// sync と import --adopt が同じ配置経路を通るように切り出してある。
// 配置の入口が 1 つしかない状態を保つことで、記録の取りこぼしを防ぐ。
func (a *App) deploy(p manifest.Package, dest string, fetched source.Fetched,
	opts linker.Options, origin string) (linker.Result, error) {

	root := fetched.Root

	// auto は配置の直前に解決する。記録には解決後の値を書くので、
	// 撤去がどちらの経路を使うかは state が決まりきって決まる。
	strategy := p.Strategy
	if strategy == manifest.StrategyAuto {
		strategy = resolveStrategy(dest)
	}

	var res linker.Result
	var deployed string
	var err error

	switch strategy {
	case manifest.StrategyCopy:
		req := linker.CopyRequest{
			Dest: dest, Src: root,
			Force: opts.Force, BackupDir: opts.BackupDir, Stamp: opts.Stamp, Adopt: opts.Adopt,
		}
		// 前回の記録を判定に使うのは、配置先が動いていないときだけ。
		// 別の場所のダイジェストを持ち込むと、まったく無関係な内容を
		// 「kata が置いたもの」と誤認しかねない。
		if prev, ok := a.st.Get(origin, p.Name); ok && prev.Dest == dest {
			req.Known, req.LinkTarget = prev.Digest, prev.Target
		}
		res, deployed, err = linker.ApplyCopy(req)
	default:
		res, err = linker.ApplyWith(dest, root, opts)
	}
	if err != nil {
		return linker.Unchanged, err
	}

	a.lock.Put(lockfile.Entry{
		Name:    p.Name,
		Type:    p.Type,
		Source:  p.SourceID(),
		Ref:     p.Ref,
		Commit:  fetched.Commit,
		Subpath: p.Path,
		Local:   p.Local,
		// url にはコミットが無いので、内容ダイジェストがピンの役割を果たす。
		Digest: fetched.Digest,
	})
	a.st.Put(state.Entry{
		Name:     p.Name,
		Type:     p.Type,
		Dest:     dest,
		Target:   root,
		Strategy: strategy,
		Scope:    p.Scope,
		Digest:   deployed,
		Origin:   origin,
	})
	return res, nil
}

// resolveStrategy は auto を実際の戦略に解決する。
//
// 判定は配置先のディレクトリで行う。同じマシンでも、ネットワーク越しの
// ファイルシステムなど symlink を作れない場所が配置先になりうるため。
func resolveStrategy(dest string) string {
	if linker.SymlinkSupported(filepath.Dir(dest)) {
		return manifest.StrategyLink
	}
	return manifest.StrategyCopy
}

// linkOptions は sync の指定を配置時の挙動へ写す。
func (a *App) linkOptions(opts SyncOptions) linker.Options {
	return linker.Options{
		Force: opts.Force, BackupDir: a.cfg.BackupDir(), Stamp: opts.stamp, Adopt: opts.Adopt,
	}
}

// actionFor は配置結果を報告用の操作名に写す。
func actionFor(res linker.Result) Action {
	switch res {
	case linker.Created:
		return ActionCreate
	case linker.Updated:
		return ActionUpdate
	case linker.Adopted:
		// ディスクは変わっていないので unchanged として数える。
		return ActionUnchanged
	default:
		return ActionUnchanged
	}
}

// planAction は取得を伴わずに、おおよそ何が起きるかを見積もる。
func (a *App) planAction(origin, name, dest string) Action {
	prev, ok := a.st.Get(origin, name)
	if !ok {
		return ActionCreate
	}
	if prev.Strategy == manifest.StrategyCopy {
		// digest.Tree は読み取りしかしないので、dry-run から呼んでも状態を変えない。
		// SymlinkSupported は探索ファイルを作るため、ここでは決して呼ばない。
		actual, err := digest.Tree(dest)
		if err == nil && digest.Equal(actual, prev.Digest) {
			return ActionUnchanged
		}
		return ActionUpdate
	}
	if linkPointsTo(dest, prev.Target) {
		return ActionUnchanged
	}
	return ActionUpdate
}

// checkShape は取得した実体が種別に見合った形かを確認する。
// 形が違えば誤配置なのでエラー、内容の不足は警告に留める。
func (a *App) checkShape(p manifest.Package, root string) (string, error) {
	fi, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	wantDir := a.resolver.ExpectDir(p.Type)
	if wantDir && !fi.IsDir() {
		return "", fmt.Errorf("package %q: type %q expects a directory but %s is a file", p.Name, p.Type, root)
	}
	if !wantDir && fi.IsDir() {
		return "", fmt.Errorf("package %q: type %q expects a file but %s is a directory", p.Name, p.Type, root)
	}
	switch p.Type {
	case manifest.TypeSkill:
		if _, err := os.Stat(filepath.Join(root, "SKILL.md")); err != nil {
			return "SKILL.md not found in the source directory", nil
		}
	case manifest.TypeAgent:
		// サブエージェントは front matter の name/description で認識されるため、
		// 無いと配置しても読み込まれない。誤配置ではないので警告に留める。
		if !hasFrontMatter(root) {
			return "front matter not found; the agent may be ignored", nil
		}
	}
	return "", nil
}

// hasFrontMatter は先頭が YAML front matter で始まるかを見る。
// 中身の妥当性までは検証しない。取り違えに気づかせるための安価な確認に留める。
func hasFrontMatter(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 4)
	if n, _ := io.ReadFull(f, head); n < 4 {
		return false
	}
	return string(head[:3]) == "---" && (head[3] == '\n' || head[3] == '\r')
}

// persist はロックと配置実績を書き出す。
func (a *App) persist() error {
	if err := a.lock.Save(a.cfg.LockPath(), time.Now()); err != nil {
		return err
	}
	return a.st.Save(a.cfg.StatePath())
}

// linkPointsTo は dest が target を指す symlink かを返す。
func linkPointsTo(dest, target string) bool {
	fi, err := os.Lstat(dest)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	cur, err := os.Readlink(dest)
	return err == nil && cur == target
}
