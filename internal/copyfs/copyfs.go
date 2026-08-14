// Package copyfs はディレクトリ木の複製を担う。
// import が ~/.claude の実体を local/ へ取り込むために使う。
//
// os.CopyFS を使わないのは、あれが symlink をそのまま再現してしまい、
// 取り込み先の git リポジトリにマシン固有の絶対パスが混入するため。
// 加えて import には、途中で失敗しても中途半端な木を残さないことと、
// 何を読み飛ばしたかを利用者へ報告できることが要る。
package copyfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/cutmail/kata/internal/digest"
)

const (
	// maxFiles は 1 回の複製で受け入れるファイル数の上限。skill 一式は多くても
	// 数百ファイルであり、これを大きく超えるのは node_modules のような取り込む
	// つもりのないディレクトリを掴んでいる合図なので、そこで止める。
	maxFiles = 20000
	// maxBytes は 1 回の複製で受け入れる合計バイト数の上限。複製先は git で
	// 共有されるリポジトリであり、数百 MB に達した時点で取り込む対象の選び方が
	// 誤っている。
	maxBytes = 200 << 20
)

// Skip は複製しなかった要素 1 件。
type Skip struct {
	// Path は複製元からの相対パス。
	Path   string
	Reason string
}

// Report は複製の結果。読み飛ばしたものを利用者に報告するために持つ。
type Report struct {
	Files   int
	Bytes   int64
	Skipped []Skip
	// Sensitive は本人しか読めない権限だったものの相対パス。
	//
	// 複製先は git で共有されるリポジトリであり、権限は 0644 に正規化される。
	// 0600 で置かれていたものは、そのまま push すれば内容も保護も同時に失われる。
	// 黙って運ぶのではなく、コミット前に気づけるよう報告する。
	Sensitive []string
}

// limits は 1 回の複製で受け入れる上限。テストから小さい値を渡して
// 同じ経路を通せるように、定数ではなく値として引き回す。
type limits struct {
	files int
	bytes int64
}

// defaultLimits は Dir が使う上限。
var defaultLimits = limits{files: maxFiles, bytes: maxBytes}

// Dir は src の木を dst へ複製する。dst が既に存在すればエラーを返す。
//
// 途中で失敗したときに中途半端な木を残さないよう、dst と同じ親の一時
// ディレクトリへ作ってから rename する。
//
// symlink は複製せず Report に記録する。複製先は git で共有される
// リポジトリなので、マシン固有の参照を持ち込まないため。
// 通常ファイルとディレクトリ以外（fifo, socket, device）も同様に飛ばす。
func Dir(src, dst string) (Report, error) {
	return copyDir(src, dst, defaultLimits)
}

// File は単一ファイルを複製する。実行ビットは引き継ぐ。
func File(src, dst string) (Report, error) {
	var rep Report

	fi, err := os.Lstat(src)
	if err != nil {
		return rep, err
	}
	if !fi.Mode().IsRegular() {
		return rep, fmt.Errorf("not a regular file: %s", src)
	}
	if err := ensureAbsent(dst); err != nil {
		return rep, err
	}
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return rep, err
	}

	// Dir と同じく、書き切ってから rename する。中断されたときに
	// 半分だけ書けたファイルを dst に残さないため。
	tmp, err := os.MkdirTemp(parent, ".kata-import-")
	if err != nil {
		return rep, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	if fi.Mode().Perm()&0o077 == 0 {
		rep.Sensitive = append(rep.Sensitive, filepath.Base(src))
	}
	staged := filepath.Join(tmp, filepath.Base(dst))
	n, err := copyContents(src, staged, fi.Mode())
	if err != nil {
		return rep, err
	}
	if err := os.Rename(staged, dst); err != nil {
		return rep, err
	}
	rep.Files = 1
	rep.Bytes = n
	return rep, nil
}

// copyDir は Dir の実体。上限を引数に取るのはテストのため。
func copyDir(src, dst string, lim limits) (Report, error) {
	var rep Report

	fi, err := os.Lstat(src)
	if err != nil {
		return rep, err
	}
	// symlink を辿らない方針はルートにも及ぼす。ルートだけ実体を辿ると、
	// 木の途中の symlink を飛ばす扱いと食い違って挙動が読めなくなる。
	if !fi.IsDir() {
		return rep, fmt.Errorf("not a directory: %s", src)
	}
	if err := ensureAbsent(dst); err != nil {
		return rep, err
	}
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return rep, err
	}

	// 一時ディレクトリを dst と同じ親に置くのは、別のファイルシステムを
	// またぐと rename が失敗するため。
	tmp, err := os.MkdirTemp(parent, ".kata-import-")
	if err != nil {
		return rep, err
	}
	// 成功した場合 tmp は rename 済みで存在しないので、この片付けは無害。
	defer func() { _ = os.RemoveAll(tmp) }()

	if err := walk(src, tmp, lim, &rep); err != nil {
		return rep, err
	}
	// MkdirTemp は 0700 で作る。そのまま移すと中を辿れなくなるので直す。
	if err := os.Chmod(tmp, 0o755); err != nil {
		return rep, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return rep, err
	}
	return rep, nil
}

// walk は src の木を辿って dst の下へ写す。
// 複製できない種別はエラーにせず Report に記録して走査を続ける。
// 何を取り込めなかったかを最後にまとめて報告したいため。
func walk(src, dst string, lim limits, rep *Report) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			// 複製先のルートは一時ディレクトリとして既にある。
			return nil
		}
		out := filepath.Join(dst, rel)

		switch mode := d.Type(); {
		case mode&fs.ModeSymlink != 0:
			// リンク先はこのマシンでしか意味を持たない。WalkDir は symlink を
			// 辿らないので、記録するだけで走査ループも起きない。
			rep.Skipped = append(rep.Skipped, Skip{Path: rel, Reason: "symlink"})
			return nil
		case mode.IsDir():
			if err := os.Mkdir(out, 0o755); err != nil {
				return err
			}
			// umask に左右されず 0755 に揃える。
			return os.Chmod(out, 0o755)
		case mode.IsRegular():
			return copyRegular(path, out, rel, d, lim, rep)
		default:
			// fifo・socket・device は複製しても意味がなく、開こうとすると
			// 環境次第でブロックしたり失敗したりする。
			rep.Skipped = append(rep.Skipped, Skip{Path: rel, Reason: "irregular file"})
			return nil
		}
	})
}

// copyRegular は上限を確かめてから 1 ファイルを写し、Report を進める。
func copyRegular(src, dst, rel string, d fs.DirEntry, lim limits, rep *Report) error {
	fi, err := d.Info()
	if err != nil {
		return err
	}
	// 本人しか読めない権限だったものは、複製先で 0644 に緩む。
	// 取り込んだ内容をそのままコミットすると、内容も保護も同時に失われる。
	if fi.Mode().Perm()&0o077 == 0 {
		rep.Sensitive = append(rep.Sensitive, rel)
	}
	if rep.Files+1 > lim.files {
		return fmt.Errorf("refusing to copy more than %d files: %s", lim.files, src)
	}
	// 読む前に大きさで弾く。書き終えてから気づくのでは遅い。
	if rep.Bytes+fi.Size() > lim.bytes {
		return fmt.Errorf("refusing to copy more than %d bytes: %s", lim.bytes, src)
	}
	n, err := copyContents(src, dst, fi.Mode())
	if err != nil {
		return err
	}
	rep.Files++
	rep.Bytes += n
	return nil
}

// copyContents は 1 ファイルの中身を写し、書き込んだバイト数を返す。
func copyContents(src, dst string, mode fs.FileMode) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()

	// 書き込み先は作りたての一時ディレクトリの中で衝突しえない。
	// それでも O_EXCL にするのは、想定外の衝突を黙って上書きしないため。
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permFor(mode))
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, err
	}
	// OpenFile の権限は umask で削られる。実行ビットの有無は skill 同梱の
	// スクリプトの動作を左右するので、明示的に指定し直す。
	if err := os.Chmod(dst, permFor(mode)); err != nil {
		return n, err
	}
	return n, nil
}

// permFor は複製先の権限を決める。
//
// 正規化の規則は digest と共有する。ここが digest.Tree の前提とずれると、
// copy 戦略で配置した直後の内容ダイジェストが記録と食い違い、
// sync が毎回 Updated を返して冪等性が壊れる。
func permFor(mode fs.FileMode) fs.FileMode { return digest.SanitizeMode(mode) }

// ensureAbsent は複製先が空いていることを確かめる。
// 既にあるものへ書き足すと、取り込み結果が複製元と一致しなくなる。
func ensureAbsent(dst string) error {
	_, err := os.Lstat(dst)
	if err == nil {
		return fmt.Errorf("destination already exists: %s", dst)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
