// Package linker は配置先への symlink 作成と撤去を担う。
//
// 不変条件: kata が作っていないものは決して削除・上書きしない。
// 配置は一時リンクを作ってから rename することで、常に原子的に切り替わる。
package linker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Result は配置操作の結果。
type Result int

const (
	// Unchanged は既に目的の状態だったことを示す。
	Unchanged Result = iota
	// Created は新たに配置したことを示す。
	Created
	// Updated はリンク先を張り替えたことを示す。
	Updated
	// Adopted は配置先に触れないまま、管理下として引き取ったことを示す。
	// ディスクは変わらないが、以後 kata が撤去できるようになる。
	Adopted
)

// ErrOccupied は配置先に kata 管理外の実体が存在することを示す。
var ErrOccupied = errors.New("destination is occupied by a non-kata file")

// Options は配置時の挙動。
type Options struct {
	// Force が真なら、配置先の kata 管理外の実体を退避してから配置する。
	Force bool
	// BackupDir は退避先のルート。
	BackupDir string
	// Stamp は退避先のサブディレクトリ名。
	// 1 回の実行で複数を退避するとき、呼び出し側が一度だけ作って渡すと
	// 「今回どけたものが全部ここにある」と言える。空なら呼び出し時刻から作る。
	Stamp string
	// Adopt は copy 戦略でのみ意味を持つ。配置先の内容がこれから置く内容と
	// 完全に同じ場合に、ファイルには触れないまま管理下として引き取る。
	Adopt bool
}

// NewStamp は退避先のサブディレクトリ名を作る。
// 秒精度なので、実行をまたいで衝突しないよう呼び出し側が一度だけ作って使い回す。
func NewStamp() string { return time.Now().UTC().Format("20060102-150405") }

// Apply は ApplyWith を従来の引数形で呼ぶ。
func Apply(dest, target string, force bool, backupDir string) (Result, error) {
	return ApplyWith(dest, target, Options{Force: force, BackupDir: backupDir})
}

// ApplyWith は dest が target を指す symlink になるようにする。
// 配置先に実ファイルや実ディレクトリがある場合は上書きせずエラーを返す。
// opts.Force が真の場合のみ退避してから配置する。
func ApplyWith(dest, target string, opts Options) (Result, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return Unchanged, err
	}

	fi, err := os.Lstat(dest)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := swap(dest, target); err != nil {
			return Unchanged, err
		}
		return Created, nil
	case err != nil:
		return Unchanged, err
	}

	if fi.Mode()&os.ModeSymlink != 0 {
		current, err := os.Readlink(dest)
		if err != nil {
			return Unchanged, err
		}
		if current == target {
			return Unchanged, nil
		}
		if err := swap(dest, target); err != nil {
			return Unchanged, err
		}
		return Updated, nil
	}

	// symlink ではない実体がある。利用者のファイルの可能性があるため慎重に扱う。
	if !opts.Force {
		return Unchanged, fmt.Errorf("%w: %s", ErrOccupied, dest)
	}
	if err := backup(dest, opts.BackupDir, opts.Stamp); err != nil {
		return Unchanged, err
	}
	if err := swap(dest, target); err != nil {
		return Unchanged, err
	}
	return Updated, nil
}

// Remove は kata が張ったリンクだけを取り除く。
// リンク先が期待と異なる場合や実体だった場合は何もしない。
func Remove(dest, expectedTarget string) (bool, error) {
	fi, err := os.Lstat(dest)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return false, nil
	}
	current, err := os.Readlink(dest)
	if err != nil {
		return false, err
	}
	if expectedTarget != "" && current != expectedTarget {
		return false, nil
	}
	if err := os.Remove(dest); err != nil {
		return false, err
	}
	return true, nil
}

// IsBrokenLink は壊れた symlink かどうかを返す。
func IsBrokenLink(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	_, err = os.Stat(path)
	return err != nil
}

// SymlinkSupported は指定ディレクトリで symlink を作成できるかを試す。
// Windows など作成できない環境をあらかじめ検出するために使う。
func SymlinkSupported(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".kata-symlink-probe")
	_ = os.Remove(probe)
	if err := os.Symlink(dir, probe); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

// swap は一時リンクを作ってから rename することで原子的に張り替える。
func swap(dest, target string) error {
	tmp := dest + ".kata-tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// backup は既存の実体を退避先へ移す。
// stamp が空なら呼び出し時刻から作る。
func backup(path, backupDir, stamp string) error {
	if backupDir == "" {
		return fmt.Errorf("backup directory is not configured")
	}
	if stamp == "" {
		stamp = NewStamp()
	}
	dir := filepath.Join(backupDir, stamp)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst, err := reserve(dir, filepath.Base(path))
	if err != nil {
		return err
	}
	// 予約した目印を外してから、その名前へ移す。目印は同じディレクトリにあるので
	// この隙間に別のプロセスが同名を作ることはない（作れば予約に失敗している）。
	if err := os.Remove(dst); err != nil {
		return err
	}
	return os.Rename(path, dst)
}

// reserve は dir の中の空き名を確保して返す。
//
// 退避は「削除はしない」という保証の実装なので、同じ退避先に同名の実体が
// 集まったときに rename が黙って上書きしてしまうと保証が崩れる。
// 別種別の同名パッケージや、複数リポジトリの同時退避で実際に起こりうる。
//
// 「空きを確かめてから移す」だけでは、その隙間に別のプロセスが同じ名前を
// 作りうる。O_EXCL で実際に place holder を作って確保することで、
// 確保できた時点でその名前は自分のものになる。
func reserve(dir, name string) (string, error) {
	for i := 0; i < 1000; i++ {
		candidate := filepath.Join(dir, name)
		if i > 0 {
			candidate = filepath.Join(dir, fmt.Sprintf("%s.%d", name, i))
		}
		f, err := os.OpenFile(candidate, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return candidate, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("too many backups named %q in %s", name, dir)
}
