package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StagingGrace は中断された取得の残骸を掃除しないでおく猶予。
// 別のプロセスがいま取得中である可能性があるため、作りたてには手を出さない。
const StagingGrace = time.Hour

// PruneOptions は prune の対象と挙動を制御する。
type PruneOptions struct {
	// Apply が偽なら何も消さず、消す予定だけを返す。
	// 破壊的な操作なので、既定は「何もしない」側に倒してある。
	Apply bool
	// Store は lock からも配置実績からも参照されていないキャッシュを対象にする。
	Store bool
	// Staging は中断された取得の残骸を対象にする。
	Staging bool
	// State は配置先を失った記録を対象にする。
	State bool
	// OlderThan は指定より新しいものを対象から外す。0 なら経過時間を見ない。
	OlderThan time.Duration
}

// PruneKind は掃除対象の種類。
type PruneKind string

const (
	// PruneStore は取得物のキャッシュ。
	PruneStore PruneKind = "store"
	// PruneStaging は中断された取得の残骸。
	PruneStaging PruneKind = "staging"
	// PruneState は配置先を失った記録。
	PruneState PruneKind = "state"
)

// PruneItem は掃除対象 1 件。
type PruneItem struct {
	Kind PruneKind `json:"kind"`
	Path string    `json:"path"`
	// Reason はなぜ不要と判断したか。
	Reason  string `json:"reason"`
	Bytes   int64  `json:"bytes"`
	Removed bool   `json:"removed"`
	Err     error  `json:"-"`
}

// PruneReport は prune 全体の結果。
type PruneReport struct {
	Items []PruneItem `json:"items"`
	// Applied が偽なら、実際には何も消していない。
	Applied bool `json:"applied"`
	// Bytes は対象の合計バイト数。
	Bytes int64 `json:"bytes"`
	// BackupCount と BackupBytes は退避物の量。kata は退避物を消さないので、
	// 判断材料として知らせるだけ。
	BackupCount int    `json:"backup_count"`
	BackupBytes int64  `json:"backup_bytes"`
	BackupDir   string `json:"backup_dir"`
}

// Prune は不要になったキャッシュと記録を掃除する。
//
// ~/.kata/backups と配置先には決して触れない。退避物は「--force のときだけ
// 退避する、削除はしない」という約束で預かった利用者自身のファイルであり、
// kata がそれを消す手段を持つこと自体が約束に反する。
// ネットワークは使わないが、他のコマンドと呼び出し方を揃えて ctx を受ける。
func (a *App) Prune(ctx context.Context, opts PruneOptions) (*PruneReport, error) {
	if !opts.Apply {
		return a.prune(opts)
	}
	var rep *PruneReport
	var opErr error
	if err := a.mutate(func() error {
		rep, opErr = a.prune(opts)
		return nil
	}); err != nil {
		return nil, err
	}
	return rep, opErr
}

func (a *App) prune(opts PruneOptions) (*PruneReport, error) {
	// 組み立てを誤ったときに想定外の場所を消さないよう、入口で足場を確かめる。
	if err := a.checkPruneRoot(); err != nil {
		return nil, err
	}
	// 何も指定されなければ、安全な 2 つだけを既定にする。
	// state は記録を失うと撤去ができなくなるため、必ず明示を求める。
	if !opts.Store && !opts.Staging && !opts.State {
		opts.Store, opts.Staging = true, true
	}

	rep := &PruneReport{Applied: opts.Apply, BackupDir: a.cfg.BackupDir()}

	if opts.Store {
		a.pruneStore(rep, opts)
	}
	if opts.Staging {
		a.pruneStaging(rep, opts)
	}
	if opts.State {
		a.pruneState(rep, opts)
	}
	for _, it := range rep.Items {
		rep.Bytes += it.Bytes
	}
	a.countBackups(rep)

	if opts.Apply && (opts.State || opts.Store || opts.Staging) {
		if err := a.st.Save(a.cfg.StatePath()); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// checkPruneRoot は掃除の足場が妥当かを確かめる。
func (a *App) checkPruneRoot() error {
	root := a.cfg.KataHome
	if root == "" {
		return fmt.Errorf("the kata home directory is not configured")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	// ルート直下やファイルシステムの根を掃除の起点にはできない。
	if abs == string(filepath.Separator) || filepath.Dir(abs) == abs {
		return fmt.Errorf("refusing to prune %q", abs)
	}
	return nil
}

// pruneStore は参照されていない取得物のキャッシュを対象にする。
func (a *App) pruneStore(rep *PruneReport, opts PruneOptions) {
	root := a.store.ReposRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	live := a.liveStoreKeys()

	for _, e := range entries {
		if live[e.Name()] {
			continue
		}
		// ReadDir が返した名前をそのまま消さず、必ず自分で組み立て直す。
		path := filepath.Join(root, e.Name())
		if !safeUnder(root, path) {
			continue
		}
		if !olderThan(path, opts.OlderThan) {
			continue
		}
		rep.add(a.removeIfApplying(PruneItem{
			Kind: PruneStore, Path: path, Bytes: dirSize(path),
			Reason: "not referenced by any lock or deployment",
		}, opts.Apply))
	}
}

// pruneStaging は中断された取得の残骸を対象にする。
func (a *App) pruneStaging(rep *PruneReport, opts PruneOptions) {
	root := a.store.StagingRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		// 取得中の一時ディレクトリだけを対象にする。
		if !strings.HasPrefix(e.Name(), "fetch-") {
			continue
		}
		path := filepath.Join(root, e.Name())
		if !safeUnder(root, path) {
			continue
		}
		// 別のプロセスが取得中かもしれないので、作りたてには手を出さない。
		grace := opts.OlderThan
		if grace < StagingGrace {
			grace = StagingGrace
		}
		if !olderThan(path, grace) {
			continue
		}
		rep.add(a.removeIfApplying(PruneItem{
			Kind: PruneStaging, Path: path, Bytes: dirSize(path),
			Reason: "interrupted fetch",
		}, opts.Apply))
	}
}

// pruneState は配置先を失った記録を落とす。
//
// この経路はファイルシステムに一切触れない。state.json を書き換えるだけで、
// 配置先の中身は読むことすらしない。
func (a *App) pruneState(rep *PruneReport, opts PruneOptions) {
	for _, d := range a.deadStateEntries() {
		item := PruneItem{
			Kind: PruneState, Path: d.Dest,
			Reason: fmt.Sprintf("%s: the recorded destination no longer exists", d.Name),
		}
		if opts.Apply {
			a.st.Delete(d.Origin, d.Name)
			item.Removed = true
		}
		rep.add(item)
	}
}

// removeIfApplying は Apply が指定されているときだけ実際に取り除く。
func (a *App) removeIfApplying(item PruneItem, apply bool) PruneItem {
	if !apply {
		return item
	}
	if err := os.RemoveAll(item.Path); err != nil {
		item.Err = err
		return item
	}
	item.Removed = true
	return item
}

// countBackups は退避物の量を数える。消さずに知らせるだけ。
func (a *App) countBackups(rep *PruneReport) {
	entries, err := os.ReadDir(a.cfg.BackupDir())
	if err != nil {
		return
	}
	rep.BackupCount = len(entries)
	rep.BackupBytes = dirSize(a.cfg.BackupDir())
}

func (r *PruneReport) add(it PruneItem) { r.Items = append(r.Items, it) }

// Counts は種類ごとの件数を返す。
func (r *PruneReport) Counts() map[PruneKind]int {
	m := map[PruneKind]int{}
	for _, it := range r.Items {
		m[it.Kind]++
	}
	return m
}

// Failed は失敗した件数を返す。
func (r *PruneReport) Failed() int {
	n := 0
	for _, it := range r.Items {
		if it.Err != nil {
			n++
		}
	}
	return n
}

// olderThan は path が指定した時間より古いかを返す。d が 0 なら常に真。
func olderThan(path string, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(fi.ModTime()) >= d
}
