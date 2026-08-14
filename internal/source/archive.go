package source

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cutmail/kata/internal/digest"
)

// ErrUnsupportedArchive は対応していない書庫形式であることを示す。
var ErrUnsupportedArchive = errors.New("unsupported archive format")

const (
	// maxArchiveBytes は展開後の合計サイズの上限。
	maxArchiveBytes = 256 << 20
	// maxArchiveEntries はエントリ数の上限。
	maxArchiveEntries = 20000
)

// extract は archivePath の書庫を destDir の下へ展開する。
// destDir は呼び出し側が用意した空のディレクトリであること。
//
// 形式はマジックバイトで判別する（拡張子は信用しない）。
// 対応するのは gzip 圧縮 tar と zip、および無圧縮 tar。
//
// 書庫の出所は url 取得元、すなわち信用できない第三者でありうる。
// エントリ名・種別・大きさはいずれも攻撃者が自由に決められる値なので、
// ここでの検査だけが後段（copy 戦略や symlink の配置）を守る防壁になる。
func extract(archivePath, destDir string) error {
	return extractWithLimits(archivePath, destDir, maxArchiveEntries, maxArchiveBytes)
}

// extractWithLimits は extract の実体。上限を引数に取るのはテストのため。
// 本番の上限のままでは検体の生成が現実的でなく、上限を跨ぐ経路を一度も通せなくなる。
func extractWithLimits(archivePath, destDir string, maxEntries int, maxBytes int64) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return err
	}
	format, err := detectFormat(f)
	if err != nil {
		return err
	}
	if format == formatUnknown {
		return fmt.Errorf("%s: %w", filepath.Base(archivePath), ErrUnsupportedArchive)
	}

	ex := &extractor{dest: destDir, maxEntries: maxEntries, maxBytes: maxBytes}
	if err := ex.run(f, fi.Size(), format); err != nil {
		// 拒否したのに書きかけの実体が残っていては防御にならない。
		// destDir 自体は呼び出し側が用意したものなので消さず、中身だけを片付ける。
		clearDir(destDir)
		return err
	}
	return nil
}

// archiveFormat はマジックバイトで判別した書庫の形式。
type archiveFormat int

const (
	formatUnknown archiveFormat = iota
	formatGzip
	formatZip
	formatTar
)

const (
	// tarMagicOffset は tar ヘッダ中で tarMagic が置かれる位置。
	tarMagicOffset = 257
	// tarMagic は無圧縮 tar を見分けるためのマジック。
	tarMagic = "ustar"
)

// detectFormat は先頭バイト列から形式を判別する。判別できなければ formatUnknown を返す。
//
// 拡張子や URL は取得元が好きに名乗れるので判別には使わない。
// 「.tar.gz という名前の zip」を tar として読ませる余地を残さないため。
func detectFormat(r io.ReaderAt) (archiveFormat, error) {
	head := make([]byte, tarMagicOffset+len(tarMagic))
	n, err := r.ReadAt(head, 0)
	// マジックより短いファイルは EOF になるが、読めた分だけで判別できることがある。
	if err != nil && !errors.Is(err, io.EOF) {
		return formatUnknown, err
	}
	head = head[:n]

	switch {
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return formatGzip, nil
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		return formatZip, nil
	case len(head) >= tarMagicOffset+len(tarMagic) && bytes.HasPrefix(head[tarMagicOffset:], []byte(tarMagic)):
		return formatTar, nil
	}
	return formatUnknown, nil
}

// extractor は 1 回の展開の状態。
//
// 上限は書庫の自己申告（zip の UncompressedSize64、tar の Header.Size）ではなく
// 実際に読めたバイト数で数える。申告値は攻撃者が決める値であり、それを信じた時点で
// 上限は上限でなくなる。エントリを跨いだ合計で見る必要があるので状態として持つ。
type extractor struct {
	dest       string
	maxEntries int
	maxBytes   int64
	entries    int
	bytes      int64
}

// run は判別済みの形式に応じて展開する。
func (e *extractor) run(f *os.File, size int64, format archiveFormat) error {
	switch format {
	case formatGzip:
		// detectFormat は ReadAt で覗くだけでファイル位置を動かさないため、
		// 開いた直後の位置からそのまま読める。
		zr, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("read gzip archive: %w", err)
		}
		defer func() { _ = zr.Close() }()
		return e.untar(tar.NewReader(zr))
	case formatTar:
		return e.untar(tar.NewReader(f))
	case formatZip:
		zr, err := zip.NewReader(f, size)
		if err != nil {
			return fmt.Errorf("read zip archive: %w", err)
		}
		return e.unzip(zr)
	}
	// 呼び出し側が formatUnknown を弾いた後しか来ないが、網羅を型では保証できない。
	return ErrUnsupportedArchive
}

// untar は tar ストリームを展開する。
func (e *extractor) untar(tr *tar.Reader) error {
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}
		// git archive が作る tarball は先頭に pax グローバルヘッダを置く。
		// 実体を持たないメタデータなので、種別の検査に掛ける前に読み飛ばす。
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if err := e.countEntry(); err != nil {
			return err
		}
		// 名前の検査は本体を読む前に済ませる。1 バイトも書かずに拒否できることが要点。
		target, err := containedPath(e.dest, hdr.Name)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := e.mkdirAll(target); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := e.writeFile(target, tr, hdr.FileInfo().Mode()); err != nil {
				return err
			}
		case tar.TypeSymlink, tar.TypeLink:
			// リンク先はツリー外を指しうる。展開後にコピーや配置がそれを辿ってしまう。
			// 「ツリー内に閉じたリンクなら許す」には展開後にリンクグラフ全体を
			// 検証する必要があり、スキルパッケージに払わせる代償として見合わない。
			return fmt.Errorf("archive contains a symlink (%s); symlinks are not supported", hdr.Name)
		default:
			// fifo・device・socket。スキルパッケージに含まれる理由がない。
			return fmt.Errorf("archive contains an unsupported entry type %q (%s)", rune(hdr.Typeflag), hdr.Name)
		}
	}
}

// unzip は zip 書庫を展開する。
func (e *extractor) unzip(zr *zip.Reader) error {
	for _, zf := range zr.File {
		if err := e.countEntry(); err != nil {
			return err
		}
		target, err := containedPath(e.dest, zf.Name)
		if err != nil {
			return err
		}

		mode := zf.Mode()
		// zip の symlink は「リンク先を中身に持つ通常エントリ」として格納され、
		// 種別ではなく権限ビットにしか現れない。見落とすとツリー外への参照が残る。
		if mode&fs.ModeSymlink != 0 {
			return fmt.Errorf("archive contains a symlink (%s); symlinks are not supported", zf.Name)
		}
		switch {
		case mode.IsDir():
			if err := e.mkdirAll(target); err != nil {
				return err
			}
		case mode.IsRegular():
			rc, err := zf.Open()
			if err != nil {
				return fmt.Errorf("open %s in archive: %w", zf.Name, err)
			}
			err = e.writeFile(target, rc, mode)
			_ = rc.Close()
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive contains an unsupported entry type %v (%s)", mode.Type(), zf.Name)
		}
	}
	return nil
}

// containedPath は書庫内のエントリ名を root 配下の絶対パスへ解決する。
// root の外へ出る名前は、書き出す前に拒否する。
func containedPath(root, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("archive contains an entry with an empty name")
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("archive entry name contains a NUL byte: %q", name)
	}
	// POSIX では "\" は名前に使える 1 文字でしかないが、Windows では区切りとして
	// 解釈される。"/" だけを見る検査は windows-latest ですり抜けて実害のある
	// traversal になるので、含むだけで拒否する。UNC（"\\host\share"）もここで落ちる。
	if strings.ContainsRune(name, '\\') {
		return "", fmt.Errorf("archive entry name contains a backslash: %q", name)
	}
	if strings.HasPrefix(name, "/") || filepath.IsAbs(name) || hasDriveLetter(name) {
		return "", fmt.Errorf("archive entry %q is an absolute path", name)
	}
	// ".." は Clean する前に見る。Clean は "a/../../evil" を "../evil" へ畳んでしまい、
	// どこで脱出したのかが分からなくなる。要素として現れた時点で拒否すれば足りる。
	for _, elem := range strings.Split(name, "/") {
		if elem == ".." {
			return "", fmt.Errorf("archive entry %q escapes the destination", name)
		}
	}

	joined := filepath.Join(root, filepath.FromSlash(name))
	// resolveSubpath と同じ最終防衛線。上の個別判定を抜けた組み合わせがあっても、
	// 解決後の位置が root の外なら書かせない。
	rel, err := filepath.Rel(root, joined)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the destination", name)
	}
	return joined, nil
}

// hasDriveLetter は "C:" のようなドライブ文字で始まるかを返す。
// filepath.IsAbs は POSIX 上では偽を返すため、windows-latest を守るには
// 実行プラットフォームに依らない検査が別に要る。
func hasDriveLetter(name string) bool {
	if len(name) < 2 || name[1] != ':' {
		return false
	}
	c := name[0]
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// countEntry は 1 エントリ分を数える。
func (e *extractor) countEntry() error {
	e.entries++
	if e.entries > e.maxEntries {
		return fmt.Errorf("archive contains more than %d entries", e.maxEntries)
	}
	return nil
}

// mkdirAll は dir までを作り、途中で作ったものも含めて権限を揃える。
// os.MkdirAll に渡す権限は umask で削られるため、それだけでは環境ごとに
// ディレクトリの権限がぶれ、内容ダイジェストの前提が崩れる。
func (e *extractor) mkdirAll(dir string) error {
	perm := digest.SanitizeMode(fs.ModeDir)
	rel, err := filepath.Rel(e.dest, dir)
	if err != nil {
		return err
	}
	cur := e.dest
	for _, elem := range strings.Split(filepath.ToSlash(rel), "/") {
		if elem == "." {
			continue
		}
		cur = filepath.Join(cur, elem)
		if err := os.Mkdir(cur, perm); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := os.Chmod(cur, perm); err != nil {
			return err
		}
	}
	return nil
}

// writeFile は 1 エントリの中身を書き出す。
//
// 上限は残量を io.LimitReader に渡して見張る。書庫の申告サイズを信じて事前に
// 弾くだけでは、申告と実体が食い違う検体に上限を跨がれる。
func (e *extractor) writeFile(target string, r io.Reader, mode fs.FileMode) error {
	if err := e.mkdirAll(filepath.Dir(target)); err != nil {
		return err
	}
	// 権限は必ず digest.SanitizeMode を通す。書庫側の権限をそのまま使うと
	// setuid を持ち込むうえ、内容ダイジェストが環境で揺れて copy 戦略の判定が壊れる。
	perm := digest.SanitizeMode(mode)

	// destDir は空である前提なので衝突しない。それでも O_EXCL にするのは、
	// 同じ名前を 2 度含む書庫に黙って上書きさせないため。
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	// 残量 +1 だけ読ませる。+1 まで読めたということは残量を使い切った上でまだ続きが
	// あったということなので、そこで打ち切って上限超過と判定できる。
	n, err := io.Copy(out, io.LimitReader(r, e.remaining()+1))
	cerr := out.Close()
	if err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	if err := e.consume(n); err != nil {
		return err
	}
	// OpenFile の権限は umask で削られる。実行ビットの有無は skill 同梱スクリプトの
	// 動作を左右するので、書いた後に明示的に付け直す。
	return os.Chmod(target, perm)
}

// remaining はまだ展開してよいバイト数。
func (e *extractor) remaining() int64 { return e.maxBytes - e.bytes }

// consume は実際に書き出せたバイト数を合計へ加える。
func (e *extractor) consume(n int64) error {
	e.bytes += n
	if e.bytes > e.maxBytes {
		return fmt.Errorf("archive expands to more than %d bytes", e.maxBytes)
	}
	return nil
}

// clearDir は dir の中身だけを消す。
// dir 自体は呼び出し側が用意したものなので、勝手に消して契約を変えない。
func clearDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, ent := range entries {
		_ = os.RemoveAll(filepath.Join(dir, ent.Name()))
	}
}
