// Package safepath はパスの封じ込めを担う。
//
// kata は第三者が用意したツリーを取得して利用者のホームへ配置する。取得元も、
// clone してきた kata.yml も、攻撃者が用意しうる。ところが filepath.Join は
// 純粋に字句的で symlink を解決しないため、経路の途中にリンクを 1 本仕込むだけで、
// 字句的には内側に見えるパスが実際には外を指す。
//
// 書庫の展開（internal/source/archive.go）は symlink を拒否してこれを防いでいる。
// 取得元や配置先によって守りの強さが変わらないよう、同じ判定をここへ集約する。
package safepath

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrEscapes は解決した結果が基準ディレクトリの外を指すことを示す。
var ErrEscapes = errors.New("path escapes its base directory")

// ErrSymlink は経路またはツリーに symlink が含まれることを示す。
var ErrSymlink = errors.New("path contains a symlink")

// TreeLimits は受け入れるツリーの上限。
type TreeLimits struct {
	Files int
	Bytes int64
}

// DefaultTreeLimits は取得物に課す既定の上限。
//
// 小さなリポジトリでも、入れ子のツリーを仕込めばチェックアウトは何桁も膨らむ。
// 実測では 27 KB のリポジトリが 10 万ファイルに展開された。書庫側と同じように、
// 取得の時点で歯止めをかける。実在するスキルのリポジトリは桁違いに小さい。
var DefaultTreeLimits = TreeLimits{Files: 50000, Bytes: 512 << 20}

// VerifyUnder は base から dir までの各要素が symlink でないことを確かめる。
//
// base 自身は検査しない。利用者が ~/.claude を意図して別の場所へ張るのは
// 正当な運用であり、そこまで縛ると使えなくなるため。base より内側は、
// 攻撃者が用意したリポジトリに含まれうるので許さない。
//
// まだ存在しない要素に行き当たったらそこで終える。以降は kata が作るものであり、
// 攻撃者が先回りできるのは既に存在するものだけ。
func VerifyUnder(base, dir string) error {
	rel, err := filepath.Rel(base, dir)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s", ErrEscapes, dir)
	}
	if rel == "." {
		return nil
	}

	cur := base
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlink, cur)
		}
	}
	return nil
}

// NotSymlink は path が symlink でないことを確かめる。
// 掃除の起点のように、1 点だけ確認すれば足りる場所で使う。
func NotSymlink(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlink, path)
	}
	return nil
}

// VerifyTree は root 以下に symlink や特殊ファイルが無いことを確かめる。
//
// 取得したものを配置する前に通す。symlink をそのまま配置すると、取得元が
// 用意したリンク越しに利用者のホームの任意の場所が「スキルの一部」として
// 読めてしまう。書庫の展開が symlink を拒否しているのと同じ規則を、
// git と local の取得物にも適用する。
func VerifyTree(root string) error {
	fi, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrSymlink, root)
	}
	if !fi.IsDir() {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("unsupported file type %v: %s", fi.Mode().Type(), root)
		}
		return nil
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlink, p)
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return fmt.Errorf("unsupported file type %v: %s", d.Type(), p)
		}
		return nil
	})
}

// CheckTreeSize は root 以下が上限に収まることを確かめる。
// symlink は辿らないので、リンク先の大きさは数えない。
func CheckTreeSize(root string, lim TreeLimits) error {
	files := 0
	var bytes int64
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		files++
		if files > lim.Files {
			return fmt.Errorf("refusing a tree with more than %d files: %s", lim.Files, root)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		bytes += fi.Size()
		if bytes > lim.Bytes {
			return fmt.Errorf("refusing a tree larger than %d bytes: %s", lim.Bytes, root)
		}
		return nil
	})
}
