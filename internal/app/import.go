package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cutmail/kata/internal/copyfs"
	"github.com/cutmail/kata/internal/linker"
	"github.com/cutmail/kata/internal/manifest"
	"github.com/cutmail/kata/internal/state"
)

// ImportOptions は import の挙動を制御する。
type ImportOptions struct {
	// DryRun が真なら何も書かず、取り込む予定だけを返す。
	DryRun bool
	// Adopt が真なら、取り込んだ元を退避してから配置先を kata の管理下へ置き換える。
	Adopt bool
	// Types は取り込む種別を絞る。空なら配置先が扱える全種別。
	Types []string
	// Names は取り込む名前を絞る。空なら見つかった全件。
	Names []string
}

// ImportAction は 1 候補に対する判断。
type ImportAction string

const (
	// ImportImported は取り込んだ（予定を含む）ことを示す。
	ImportImported ImportAction = "imported"
	// ImportSkipped は取り込まなかったことを示す。
	ImportSkipped ImportAction = "skipped"
	// ImportFailed は取り込もうとして失敗したことを示す。
	ImportFailed ImportAction = "failed"
)

// ImportItem は走査で見つかった候補 1 件の顛末。
type ImportItem struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Src は配置先で見つかった実体。
	Src string `json:"src"`
	// Local は kata.yml に書いた（書く予定の）local: の値。
	Local  string       `json:"local,omitempty"`
	Action ImportAction `json:"action"`
	// Reason は取り込まなかった理由。
	Reason string `json:"reason,omitempty"`
	// Notes は複製時に読み飛ばしたものなどの注意。
	Notes []string `json:"notes,omitempty"`
	Err   error    `json:"-"`
}

// ImportReport は import 全体の結果。
type ImportReport struct {
	Items  []ImportItem `json:"items"`
	DryRun bool         `json:"dry_run"`
	Adopt  bool         `json:"adopt"`
	// Orphans は取り込みに失敗した結果、local/ に残ってしまった実体。
	// 利用者が自分で片付けられるように報告する。
	Orphans []string `json:"orphans,omitempty"`
}

// Counts は判断ごとの件数を返す。
func (r *ImportReport) Counts() map[ImportAction]int {
	m := map[ImportAction]int{}
	for _, it := range r.Items {
		m[it.Action]++
	}
	return m
}

// Failed は失敗した件数を返す。
func (r *ImportReport) Failed() int { return r.Counts()[ImportFailed] }

// candidate は走査で見つかった取り込み候補。
type candidate struct {
	name string
	typ  string
	// src は配置先で見つかった実体の絶対パス。
	src string
	// sub は local/ 配下の格納先ディレクトリ名（"skills" など）。
	sub string
	// ext は実体に付く拡張子。ディレクトリ種別なら空。
	ext string
}

// localAbs は取り込み先の絶対パスを返す。
func (c candidate) localAbs(manDir string) string {
	return filepath.Join(manDir, "local", c.sub, c.name+c.ext)
}

// isDir はこの種別の実体がディレクトリかを返す。
func (c candidate) isDir() bool { return c.ext == "" }

// Import は配置先を走査し、kata 管理外の実体を local/ へ取り込む。
//
// 既定では local/ への複製と kata.yml への追記だけを行い、配置先には一切触れない。
// Adopt を指定したときだけ、取り込んだものの元を退避してから symlink へ置き換える。
//
// 処理順は「配置先に触るのを最後にする」ことを守る。マニフェストを書き切るまでは
// 配置先を読むだけなので、途中で失敗しても利用者のファイルは無傷で残る。
func (a *App) Import(ctx context.Context, opts ImportOptions) (*ImportReport, error) {
	if opts.DryRun {
		return a.importPackages(ctx, opts)
	}
	var rep *ImportReport
	var opErr error
	if err := a.mutate(func() error {
		rep, opErr = a.importPackages(ctx, opts)
		return nil
	}); err != nil {
		return nil, err
	}
	return rep, opErr
}

func (a *App) importPackages(ctx context.Context, opts ImportOptions) (*ImportReport, error) {
	rep := &ImportReport{DryRun: opts.DryRun, Adopt: opts.Adopt}

	cands, err := a.scanForImport(opts)
	if err != nil {
		return nil, err
	}

	// 1. 除外判定。ここで落ちたものは理由とともに報告する。
	managed := a.managedDests()
	claimed := map[string]bool{}
	var planned []importPlan

	for _, c := range cands {
		item := ImportItem{Name: c.name, Type: c.typ, Src: c.src}
		localAbs := c.localAbs(a.man.Dir())

		if reason := a.importSkipReason(c, localAbs, managed, claimed); reason != "" {
			item.Action, item.Reason = ImportSkipped, reason
			rep.Items = append(rep.Items, item)
			continue
		}
		rel, err := a.localRelFor(localAbs)
		if err != nil {
			item.Action, item.Err = ImportFailed, err
			rep.Items = append(rep.Items, item)
			continue
		}
		// dry-run と本番が同じ文字列を報告するよう、値はここでしか作らない。
		item.Local = rel
		item.Action = ImportImported
		claimed[c.name] = true
		rep.Items = append(rep.Items, item)
		planned = append(planned, importPlan{cand: c, localAbs: localAbs, item: len(rep.Items) - 1})
	}

	if opts.DryRun || len(planned) == 0 {
		return rep, nil
	}

	// 2. local/ へ複製する。1 件の失敗で全部を止めない。
	copied := a.copyForImport(rep, planned)
	if len(copied) == 0 {
		return rep, nil
	}

	// 3. 複製できた分だけをマニフェストへ載せ、最後に 1 回だけ書き出す。
	//    途中で書き出すと、検証に落ちたときに中途半端なマニフェストが残る。
	pkgs, err := a.declareImported(rep, copied)
	if err != nil {
		return rep, err
	}
	if len(pkgs) == 0 {
		return rep, nil
	}

	// 4. ここまで配置先は無傷。Adopt のときだけ、取り込んだものに限って置き換える。
	//    配置していない以上、記録すべきこともない。lock と state は書かずに終える。
	if !opts.Adopt {
		return rep, nil
	}
	a.adoptImported(ctx, rep, pkgs)
	if err := a.persist(); err != nil {
		return rep, err
	}
	return rep, nil
}

// importPlan は取り込むと決めた候補と、報告のどの項目に対応するか。
type importPlan struct {
	cand     candidate
	localAbs string
	item     int
	pkg      manifest.Package
}

// scanForImport は配置先を走査して取り込み候補を集める。
//
// 走査するディレクトリは配置先の解決器に問い合わせる。ここで skills や commands を
// 直書きすると、解決器を足すだけで他のエージェントに対応できるという設計が崩れる。
func (a *App) scanForImport(opts ImportOptions) ([]candidate, error) {
	var out []candidate
	for _, typ := range a.resolver.Types() {
		if len(opts.Types) > 0 && !slices.Contains(opts.Types, typ) {
			continue
		}
		dir, ext, ok := a.resolver.Location(manifest.ScopeUser, typ)
		if !ok {
			continue
		}
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			// 隠しファイルは取り込む対象になりえない。.DS_Store のような
			// ノイズを毎回「読み飛ばした」と報告しても意味がないので黙って外す。
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			name := e.Name()
			if ext != "" {
				if !strings.HasSuffix(name, ext) {
					continue
				}
				name = strings.TrimSuffix(name, ext)
			}
			if len(opts.Names) > 0 && !slices.Contains(opts.Names, name) {
				continue
			}
			// 形が合わないものは候補にしない。ただし symlink はここで落とさず、
			// 除外判定に回して「なぜ取り込まれないか」を説明する。
			isLink := e.Type()&fs.ModeSymlink != 0
			if !isLink && e.IsDir() != (ext == "") {
				continue
			}
			out = append(out, candidate{
				name: name, typ: typ, src: filepath.Join(dir, e.Name()),
				sub: filepath.Base(dir), ext: ext,
			})
		}
	}
	return out, nil
}

// managedDests は state に記録された配置先を返す。
//
// origin で絞らないのは、同じマシン上の別の kata.yml が配置したものまで
// 取り込んでしまわないため。state.json はマシン全体で 1 つなので、
// 全件を見ることで他のリポジトリの配置を横取りしない。
func (a *App) managedDests() map[string]state.Entry {
	out := make(map[string]state.Entry, len(a.st.Entries))
	for _, e := range a.st.Entries {
		out[e.Dest] = e
	}
	return out
}

// importSkipReason は候補を取り込まない理由を返す。取り込んでよければ空を返す。
//
// 判定は上から順に見る。kata が置いたものを二重に取り込まないことと、
// 利用者が既に持っているもの（宣言・local/ の実体）を書き換えないことが目的。
func (a *App) importSkipReason(c candidate, localAbs string,
	managed map[string]state.Entry, claimed map[string]bool) string {

	// 1. kata の配置実績がある。理由を具体的に説明するために先に見る。
	if e, ok := managed[c.src]; ok {
		return fmt.Sprintf("managed by kata (declared in %s)", e.Origin)
	}
	// 2. symlink であること自体で除外する。state.json を失っていても、
	//    他のツールが張ったものであっても、ここで確実に受け止める。
	//    1 が外れても安全性が落ちないよう、保証はこちらに置く。
	fi, err := os.Lstat(c.src)
	if err != nil {
		return fmt.Sprintf("cannot read: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return "already a symlink"
	}
	// 3. 取り込んでも宣言できない名前は最初から外す。
	if !manifest.ValidName(c.name) {
		return "name is not usable as a package name"
	}
	// 4. 既にある宣言は決して書き換えない。
	if _, ok := a.man.Find(c.name); ok {
		return "already declared in kata.yml; use --name to import it under a different name"
	}
	// 5. 利用者のリポジトリを壊さない。
	if _, err := os.Lstat(localAbs); err == nil {
		return fmt.Sprintf("%s already exists", filepath.Base(localAbs))
	}
	// 6. マニフェストの名前は種別をまたいで一意なので、同じ実行の中でも衝突する。
	if claimed[c.name] {
		return "name collides with another entry found in this run"
	}
	return ""
}

// copyForImport は候補を local/ へ複製する。失敗は記録して続行する。
// 配置先に 1 つ変なものがあるだけで全部の取り込みが止まるのは実用的でないため。
func (a *App) copyForImport(rep *ImportReport, planned []importPlan) []importPlan {
	var copied []importPlan
	for _, pl := range planned {
		item := &rep.Items[pl.item]

		var r copyfs.Report
		var err error
		if pl.cand.isDir() {
			r, err = copyfs.Dir(pl.cand.src, pl.localAbs)
		} else {
			r, err = copyfs.File(pl.cand.src, pl.localAbs)
		}
		if err != nil {
			item.Action, item.Err = ImportFailed, err
			continue
		}
		for _, s := range r.Skipped {
			item.Notes = append(item.Notes, fmt.Sprintf("did not copy %s (%s)", s.Path, s.Reason))
		}
		if len(r.Sensitive) > 0 {
			item.Notes = append(item.Notes, fmt.Sprintf(
				"%s was readable only by you; the copy under local/ is not, "+
					"so review it before committing", strings.Join(r.Sensitive, ", ")))
		}
		copied = append(copied, pl)
	}
	return copied
}

// declareImported は複製できた分をマニフェストへ載せ、1 回だけ書き出す。
//
// 検証に落ちたら書き出さない。読めない kata.yml を残すと全コマンドが動かなくなり、
// 手で直すしかなくなるため、そこだけは何があっても避ける。
func (a *App) declareImported(rep *ImportReport, copied []importPlan) ([]importPlan, error) {
	var pkgs []importPlan
	for _, pl := range copied {
		item := &rep.Items[pl.item]
		p, err := a.buildPackage(AddSpec{Source: pl.localAbs, Name: pl.cand.name, Type: pl.cand.typ})
		if err == nil {
			err = a.man.Add(p)
		}
		if err != nil {
			item.Action, item.Err = ImportFailed, err
			rep.Orphans = append(rep.Orphans, pl.localAbs)
			continue
		}
		pl.pkg = p
		pkgs = append(pkgs, pl)
	}
	if len(pkgs) == 0 {
		return nil, nil
	}

	if err := a.man.Validate(); err != nil {
		for _, pl := range pkgs {
			rep.Items[pl.item].Action = ImportFailed
			rep.Orphans = append(rep.Orphans, pl.localAbs)
		}
		return nil, fmt.Errorf("nothing was written to %s: %w", manifest.FileName, err)
	}
	if err := a.man.Save(a.cfg.ManifestPath); err != nil {
		return nil, err
	}
	return pkgs, nil
}

// adoptImported は取り込んだものに限って、配置先を kata の管理下へ置き換える。
//
// Sync(Force) に任せないのは、Force が取り込み対象以外にも及んで、
// 無関係なパッケージの配置先にある利用者のファイルまで退避してしまうため。
func (a *App) adoptImported(ctx context.Context, rep *ImportReport, pkgs []importPlan) {
	// 今回どけたものが 1 つのディレクトリに揃うよう、退避先の名前は一度だけ決める。
	stamp := linker.NewStamp()
	origin := a.Origin()

	for _, pl := range pkgs {
		item := &rep.Items[pl.item]

		dest, err := a.resolver.Resolve(pl.pkg)
		if err != nil {
			item.Action, item.Err = ImportFailed, err
			continue
		}
		fetched, err := a.fetch(ctx, pl.pkg, false)
		if err != nil {
			item.Action, item.Err = ImportFailed, err
			continue
		}
		opts := linker.Options{Force: true, BackupDir: a.cfg.BackupDir(), Stamp: stamp}
		if _, err := a.deploy(pl.pkg, dest, fetched, opts, origin); err != nil {
			item.Action, item.Err = ImportFailed, err
			continue
		}
		item.Notes = append(item.Notes, "moved the original into "+filepath.Join(a.cfg.BackupDir(), stamp))
	}
}
