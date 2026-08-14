// Package digest は「配置した内容が今も kata が置いたままか」を判定し、
// url 取得物が宣言どおりのバイト列かを検証するためのダイジェストを計算する。
//
// copy 戦略の撤去は配置先を丸ごと削除する。それを正当化できるのは
// 「配置した時点の内容から一切変わっていない」という証明だけであり、
// この判定が誤ると利用者の編集を消してしまう。そのため、コピーや環境で揺れる情報は
// 一切プリイメージに含めない（Tree のコメントを参照）。
package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// Prefix はダイジェスト文字列の接頭辞。将来アルゴリズムを変えたときに見分けられるようにする。
const Prefix = "sha256:"

// TreeVersion は Tree のプリイメージ書式の版。
// 書式を変えたらここを上げる。既存の state.json との突き合わせが黙って壊れるのを防ぐ。
const TreeVersion = "kata-tree-v1"

// ErrSymlink は対象に symlink が含まれることを示す。
var ErrSymlink = errors.New("the tree contains a symlink")

// Tree はディレクトリまたは単一ファイルの内容ダイジェストを返す。
//
// プリイメージは次のとおり。ここを変えると state.json に記録済みの値と突き合わなくなるため、
// 変更するときは TreeVersion も必ず上げること。
//
//	sha256(
//	  "kata-tree-v1\x00"
//	  ++ for each rel in sort(全相対パス, バイト順):
//	       dir  : "d\x00" ++ rel ++ "\x00"
//	       file : "f\x00" ++ rel ++ "\x00" ++ mode ++ "\x00" ++ decimal(size) ++ "\x00" ++ <本体バイト列>
//	)
//	→ "sha256:" ++ hex
//
// rel は root からの相対パスを filepath.ToSlash したもの、mode は SanitizeMode 適用後の
// 権限ビットを 3 桁 8 進数にした文字列（"644" または "755"）、decimal(size) は 10 進数の文字列。
// 本体バイト列の後ろには区切りを置かない（直後は次のエントリの種別 1 文字）。
//
// 書式の決めどころと、その理由:
//
//   - mtime・uid/gid・inode は含めない。コピーのたびに変わる値で判定が揺らぐと、
//     copy の撤去が使い物にならなくなる。
//   - mode は 2 値に正規化する。umask・プラットフォーム・zip の格納権限で digest が
//     ぶれると、sync が毎回 Updated を返して冪等性が壊れる。
//   - 空ディレクトリも含める。assets/ のようなディレクトリの増減が不可視になると、
//     「編集されていない」と誤判定する。
//   - 相対パスは filepath.ToSlash する。Windows と POSIX で同じ digest になること。
//   - size を含め、実際に読めたバイト数と一致しなければエラーにする。NUL 区切りだけでは、
//     ファイル本体に区切り列が現れると単射性が崩れる。ハッシュ中に対象が変化した場合も
//     これで検出できる。
//   - symlink に当たったら ErrSymlink を返す。copy 戦略は symlink を配置しない方針なので、
//     両者の定義を一致させる。
//   - 単一ファイルの root は rel = "" の 1 エントリとして扱う。ソース側のファイル名と
//     配置先名は独立なので、digest をファイル名に依存させない。
//
// 通常ファイル・ディレクトリ以外（fifo, socket, device）に当たったらエラーを返す。
func Tree(root string) (string, error) {
	entries, err := collect(root)
	if err != nil {
		return "", err
	}
	// WalkDir の訪問順は「親ディレクトリ内での名前順」でしかなく、フルパスのバイト順とは
	// 一致しない（"a" < "a.txt" なので a, a/b, a.txt の順に訪れるが、バイト順では
	// a, a.txt, a/b）。プラットフォーム差ではなく仕様差なので、必ず集めてから並べ直す。
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	h := sha256.New()
	writeFields(h, TreeVersion)
	for _, e := range entries {
		if e.dir {
			writeFields(h, "d", e.rel)
			continue
		}
		writeFields(h, "f", e.rel, modeField(e.mode), strconv.FormatInt(e.size, 10))
		if err := writeBody(h, e.path, e.size); err != nil {
			return "", err
		}
	}
	return Prefix + hex.EncodeToString(h.Sum(nil)), nil
}

// Sum は r を読み切ってダイジェストを返す。url 取得物の検証に使う。
func Sum(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return Prefix + hex.EncodeToString(h.Sum(nil)), nil
}

// Equal は 2 つのダイジェストが等しいかを返す。どちらかが空なら偽。
// 「記録が無い」を「一致した」と扱うと、未記録の配置先を撤去してしまう。
func Equal(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return a == b
}

// SanitizeMode は書き出しに使う権限を 0644 / 0755 に正規化する。
// アーカイブ側の権限をそのまま使うと setuid などを持ち込むうえ、
// 内容ダイジェストが環境で揺れて copy 戦略の判定が壊れる。
//
// アーカイブ展開とコピー書き出しは必ずこの規則を共有すること。
// 片方だけ規則が違うと、配置直後の digest が記録と食い違い、sync が毎回 Updated を返す。
func SanitizeMode(m fs.FileMode) fs.FileMode {
	// ディレクトリは中身を作れなければ意味がないので常に 0755。
	if m.IsDir() {
		return 0o755
	}
	// Perm() が返すのは下位 9 ビットだけなので、setuid/setgid/sticky はここで必ず落ちる。
	if m.Perm()&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// entry はプリイメージに載せる 1 件。
// mtime や owner を持たないのは意図的で、持たせた瞬間に判定が揺らぐ。
type entry struct {
	rel  string
	path string
	dir  bool
	mode fs.FileMode
	size int64
}

// collect は root 以下のエントリを集める。順序はここでは保証しない（呼び出し側で並べ直す）。
func collect(root string) ([]entry, error) {
	var entries []entry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// WalkDir は root 自身も lstat 済みの DirEntry で渡してくるため、
		// root が symlink の場合もここで弾ける。
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlink, path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			// root ディレクトリ自身はエントリにしない。
			if d.IsDir() {
				return nil
			}
			// root が単一ファイルのときだけ rel = "" の 1 件になる。
			rel = ""
		}
		rel = filepath.ToSlash(rel)

		if d.IsDir() {
			entries = append(entries, entry{rel: rel, dir: true})
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unsupported file type %v: %s", d.Type(), path)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		entries = append(entries, entry{rel: rel, path: path, mode: fi.Mode(), size: fi.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// writeFields は各フィールドを NUL 区切りでハッシュに書く。
// hash.Hash の Write はエラーを返さない契約なので、戻り値は見ない。
func writeFields(h hash.Hash, fields ...string) {
	for _, f := range fields {
		h.Write([]byte(f))
		h.Write([]byte{0})
	}
}

// writeBody はファイル本体をハッシュへ流し込み、読めた長さが宣言と一致するかを確かめる。
// 食い違いはハッシュ中に対象が書き換わったことを意味し、得られる値は何の証明にもならない。
func writeBody(h hash.Hash, path string, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("file changed while hashing: %s: read %d bytes, want %d", path, n, size)
	}
	return nil
}

// modeField は権限をプリイメージ用の 3 桁 8 進数にする。
func modeField(m fs.FileMode) string {
	return fmt.Sprintf("%03o", uint32(SanitizeMode(m)))
}
