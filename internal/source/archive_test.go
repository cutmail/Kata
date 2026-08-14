package source

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExtractRejectsTraversal(t *testing.T) {
	// `..\evil` は POSIX では単なるファイル名だが、CI が回す windows-latest では
	// 1 つ上のディレクトリに着地する。実行環境に関わらず拒否できていること。
	names := []string{"../evil", "/etc/evil", "a/../../evil", `..\evil`}

	for _, name := range names {
		samples := []struct {
			kind string
			path string
		}{
			{"tar.gz", writeSample(t, "escape.tar.gz", gzipBytes(t, buildTar(t, []tarEntry{{name: name, body: "pwned"}})))},
			{"zip", writeSample(t, "escape.zip", buildZip(t, []zipEntry{{name: name, body: "pwned"}}))},
		}
		for _, s := range samples {
			parent, dest := newDest(t)
			if err := extract(s.path, dest); err == nil {
				t.Fatalf("%s entry %q: err = nil, want an error", s.kind, name)
			}
			// 「エラーを返した」だけでは足りない。書き出す前に拒否できていなければ、
			// 脱出先の実体を先に踏み潰してしまう。
			assertNothingExtracted(t, parent, dest)
		}
	}
}

func TestExtractRejectsSymlinkEntries(t *testing.T) {
	// リンクより前に通常ファイルを置くのは、拒否したときに書きかけの木が
	// 残らないことまで見るため。
	cases := []struct {
		kind string
		path string
	}{
		{
			kind: "tar symlink",
			path: writeSample(t, "symlink.tar.gz", gzipBytes(t, buildTar(t, []tarEntry{
				{name: "SKILL.md", body: "body"},
				{name: "escape", typeflag: tar.TypeSymlink, linkname: "../../../etc/passwd"},
			}))),
		},
		{
			kind: "tar hardlink",
			path: writeSample(t, "hardlink.tar.gz", gzipBytes(t, buildTar(t, []tarEntry{
				{name: "SKILL.md", body: "body"},
				{name: "alias", typeflag: tar.TypeLink, linkname: "SKILL.md"},
			}))),
		},
		{
			// zip の symlink は種別ではなく権限ビットにしか現れない。
			kind: "zip symlink",
			path: writeSample(t, "symlink.zip", buildZip(t, []zipEntry{
				{name: "SKILL.md", body: "body"},
				{name: "escape", body: "../../../etc/passwd", mode: 0o777 | fs.ModeSymlink},
			})),
		},
	}

	for _, c := range cases {
		parent, dest := newDest(t)
		err := extract(c.path, dest)
		if err == nil {
			t.Fatalf("%s: err = nil, want an error", c.kind)
		}
		// 種別不明として落ちても展開はされないが、それでは利用者に理由が伝わらない。
		// リンクだと分かって断っていること。
		if !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("%s: err = %v, want it to mention symlinks", c.kind, err)
		}
		assertNothingExtracted(t, parent, dest)
	}
}

func TestExtractRejectsIrregularEntries(t *testing.T) {
	// fifo・device はスキルパッケージに含まれる理由がなく、開けば環境次第で
	// ブロックしたり失敗したりする。作る前に落とす。
	for _, typeflag := range []byte{tar.TypeFifo, tar.TypeChar, tar.TypeBlock} {
		sample := writeSample(t, "irregular.tar.gz", gzipBytes(t, buildTar(t, []tarEntry{
			{name: "SKILL.md", body: "body"},
			{name: "odd", typeflag: typeflag},
		})))
		parent, dest := newDest(t)
		if err := extract(sample, dest); err == nil {
			t.Fatalf("tar entry type %q: err = nil, want an error", rune(typeflag))
		}
		assertNothingExtracted(t, parent, dest)
	}
}

func TestExtractEnforcesEntryLimit(t *testing.T) {
	samples := []string{
		writeSample(t, "many.tar.gz", gzipBytes(t, buildTar(t, []tarEntry{
			{name: "a.txt", body: "a"},
			{name: "b.txt", body: "b"},
			{name: "c.txt", body: "c"},
		}))),
		writeSample(t, "many.zip", buildZip(t, []zipEntry{
			{name: "a.txt", body: "a"},
			{name: "b.txt", body: "b"},
			{name: "c.txt", body: "c"},
		})),
	}

	for _, sample := range samples {
		// 本番の上限で 20000 エントリの検体を作るのは現実的でないので、
		// 同じ経路を小さい上限で通す。
		parent, dest := newDest(t)
		if err := extractWithLimits(sample, dest, 2, 1<<20); err == nil {
			t.Fatalf("%s: extracting more entries than the limit must fail", filepath.Base(sample))
		}
		assertNothingExtracted(t, parent, dest)

		// 上限ちょうどは通ること。常に失敗するだけの実装と区別する。
		_, dest = newDest(t)
		if err := extractWithLimits(sample, dest, 3, 1<<20); err != nil {
			t.Fatalf("%s: %v", filepath.Base(sample), err)
		}
		assertFile(t, filepath.Join(dest, "c.txt"), "c", false)
	}

	// 既定の上限は現実の skill 一式を通すだけの余裕があること。
	if maxArchiveEntries < 1000 {
		t.Fatalf("maxArchiveEntries = %d, want room for a realistic skill tree", maxArchiveEntries)
	}
}

func TestExtractEnforcesByteLimit(t *testing.T) {
	chunk := strings.Repeat("x", 40)

	// 1 件ずつは上限に収まるが合計では超える。エントリ単位でしか見ない実装を落とす。
	cumulative := writeSample(t, "sum.tar.gz", gzipBytes(t, buildTar(t, []tarEntry{
		{name: "a.txt", body: chunk},
		{name: "b.txt", body: chunk},
		{name: "c.txt", body: chunk},
	})))
	parent, dest := newDest(t)
	if err := extractWithLimits(cumulative, dest, 100, 64); err == nil {
		t.Fatal("extracting more bytes than the limit must fail")
	}
	assertNothingExtracted(t, parent, dest)

	// 展開後サイズを詐称した zip。申告値（1 バイト）を信じる実装はこれを上限内と
	// 判断して通してしまうが、実際に読むのは 5000 バイトで上限を超える。
	forged := writeSample(t, "forged.zip", buildZip(t, []zipEntry{
		{name: "big.txt", body: strings.Repeat("x", 5000), declaredSize: 1},
	}))
	parent, dest = newDest(t)
	if err := extractWithLimits(forged, dest, 100, 64); err == nil {
		t.Fatal("extracting a zip that understates its uncompressed size must fail")
	}
	assertNothingExtracted(t, parent, dest)

	// 上限内なら通ること。
	_, dest = newDest(t)
	if err := extractWithLimits(cumulative, dest, 100, 1<<20); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(dest, "a.txt"), chunk, false)

	if maxArchiveBytes < 1<<20 {
		t.Fatalf("maxArchiveBytes = %d, want room for a realistic skill tree", maxArchiveBytes)
	}
}

// TestExtractStopsWritingAtTheByteLimit は、1 エントリのコピーが残量で打ち切られる
// ことを確かめる。読み切ってから合計で気づく実装は、1 エントリが果てしなく展開される
// 書庫（zip bomb）に対して上限を守れない。
func TestExtractStopsWritingAtTheByteLimit(t *testing.T) {
	const limit = 64
	_, dest := newDest(t)
	ex := &extractor{dest: dest, maxEntries: 10, maxBytes: limit}

	target := filepath.Join(dest, "bomb.txt")
	// 上限をはるかに超える中身を渡す。テスト側で長さを区切っておくのは、
	// 打ち切りが壊れたときにハングではなく大きさの不一致として落ちるようにするため。
	if err := ex.writeFile(target, io.LimitReader(endlessReader{}, 1<<20), 0o644); err == nil {
		t.Fatal("copying an entry larger than the limit must fail")
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	// 上限を超えたことを知るには 1 バイトだけ余分に読む必要がある。それ以上は許さない。
	if fi.Size() > limit+1 {
		t.Fatalf("bytes written = %d, want at most %d", fi.Size(), limit+1)
	}
}

// endlessReader は上限の検証で使う、十分に長い中身を返すリーダ。
type endlessReader struct{}

func (endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestExtractNormalizesModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not modelled on this platform")
	}
	sample := writeSample(t, "modes.tar.gz", gzipBytes(t, buildTar(t, []tarEntry{
		{name: "bin/", typeflag: tar.TypeDir, mode: 0o700},
		{name: "bin/run.sh", mode: 0o777, body: "#!/bin/sh\n"},
		{name: "bin/setuid.sh", mode: 0o4755, body: "#!/bin/sh\n"},
		{name: "SKILL.md", mode: 0o600, body: "body"},
	})))
	_, dest := newDest(t)
	if err := extract(sample, dest); err != nil {
		t.Fatal(err)
	}

	// 書庫側の権限をそのまま使うと setuid を持ち込むうえ、内容ダイジェストが
	// 環境で揺れて copy 戦略の判定が壊れる。2 値に潰れていること。
	want := map[string]fs.FileMode{
		"bin":           0o755,
		"bin/run.sh":    0o755,
		"bin/setuid.sh": 0o755,
		"SKILL.md":      0o644,
	}
	for rel, wantPerm := range want {
		fi, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("lstat %s: %v", rel, err)
		}
		if got := fi.Mode().Perm(); got != wantPerm {
			t.Fatalf("permissions of %s = %04o, want %04o", rel, got, wantPerm)
		}
		if extra := fi.Mode() & (fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky); extra != 0 {
			t.Fatalf("mode of %s = %v, want no setuid/setgid/sticky bits", rel, fi.Mode())
		}
	}
}

func TestExtractTarGz(t *testing.T) {
	sample := writeSample(t, "skill.tar.gz", gzipBytes(t, buildTar(t, skillTar())))
	_, dest := newDest(t)
	if err := extract(sample, dest); err != nil {
		t.Fatal(err)
	}
	assertSkillTree(t, dest)
}

func TestExtractPlainTar(t *testing.T) {
	// 拡張子は取得元が好きに名乗れる。マジックバイトだけで無圧縮 tar と判別できること。
	sample := writeSample(t, "skill.bin", buildTar(t, skillTar()))
	_, dest := newDest(t)
	if err := extract(sample, dest); err != nil {
		t.Fatal(err)
	}
	assertSkillTree(t, dest)
}

func TestExtractZip(t *testing.T) {
	sample := writeSample(t, "skill.bin", buildZip(t, []zipEntry{
		{name: "skill/", mode: 0o755 | fs.ModeDir},
		{name: "skill/SKILL.md", body: "hello\n"},
		{name: "skill/scripts/run.sh", body: "#!/bin/sh\n", mode: 0o755},
		{name: "skill/assets/nested/note.txt", body: "deep"},
	}))
	_, dest := newDest(t)
	if err := extract(sample, dest); err != nil {
		t.Fatal(err)
	}
	assertSkillTree(t, dest)
}

func TestExtractRejectsUnknownFormat(t *testing.T) {
	// 名前が .tar.gz でも中身が書庫でなければ拒否する。
	for _, body := range [][]byte{[]byte("this is not an archive\n"), nil} {
		sample := writeSample(t, "skill.tar.gz", body)
		_, dest := newDest(t)
		if err := extract(sample, dest); !errors.Is(err, ErrUnsupportedArchive) {
			t.Fatalf("err = %v, want %v", err, ErrUnsupportedArchive)
		}
	}
}

func TestContainedPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dest")

	// 書き出す前に落とさなければならない名前。書庫の作り手が自由に決められる値なので、
	// 実行プラットフォームに関わらず同じ判定になること。
	rejected := []string{
		"",
		"a\x00b",
		`..\evil`,
		`a\..\..\evil`,
		`\\host\share\evil`,
		"/etc/evil",
		"C:/evil",
		"c:evil",
		"../evil",
		"a/../../evil",
		// ツリー内に着地する ".." も許さない。許す理由が無い一方、判定は難しくなる。
		"a/../b",
	}
	for _, name := range rejected {
		if got, err := containedPath(root, name); err == nil {
			t.Fatalf("containedPath(root, %q) = %q, want an error", name, got)
		}
	}

	// 実在の tarball は "./" 始まりのエントリを含む。これを弾いてはならない。
	accepted := map[string]string{
		"SKILL.md":  filepath.Join(root, "SKILL.md"),
		"a/b/c.txt": filepath.Join(root, "a", "b", "c.txt"),
		"dir/":      filepath.Join(root, "dir"),
		"./a.txt":   filepath.Join(root, "a.txt"),
		"./":        root,
		".":         root,
	}
	for name, want := range accepted {
		got, err := containedPath(root, name)
		if err != nil {
			t.Fatalf("containedPath(root, %q): %v", name, err)
		}
		if got != want {
			t.Fatalf("containedPath(root, %q) = %q, want %q", name, got, want)
		}
	}
}

// skillTar は正常系で使う skill 一式の tar エントリ。
func skillTar() []tarEntry {
	return []tarEntry{
		// git archive（GitHub の tarball もこれ）は先頭に pax グローバルヘッダを置く。
		// 種別の検査で弾いてしまうと、実在の tarball がひとつも展開できなくなる。
		{name: "pax_global_header", typeflag: tar.TypeXGlobalHeader},
		{name: "skill/", typeflag: tar.TypeDir},
		{name: "skill/SKILL.md", body: "hello\n"},
		{name: "skill/scripts/", typeflag: tar.TypeDir},
		{name: "skill/scripts/run.sh", mode: 0o755, body: "#!/bin/sh\n"},
		// ディレクトリエントリを持たない書庫もある。親を自分で作れること。
		{name: "skill/assets/nested/note.txt", body: "deep"},
	}
}

// assertSkillTree は skillTar と同じ内容が展開されていることを確かめる。
func assertSkillTree(t *testing.T, dest string) {
	t.Helper()
	assertFile(t, filepath.Join(dest, "skill", "SKILL.md"), "hello\n", false)
	// 実行ビットの保存は skill 同梱スクリプトのために必須。
	assertFile(t, filepath.Join(dest, "skill", "scripts", "run.sh"), "#!/bin/sh\n", true)
	assertFile(t, filepath.Join(dest, "skill", "assets", "nested", "note.txt"), "deep", false)

	for _, rel := range []string{"skill", filepath.Join("skill", "assets", "nested")} {
		fi, err := os.Lstat(filepath.Join(dest, rel))
		if err != nil {
			t.Fatalf("lstat %s: %v", rel, err)
		}
		if !fi.IsDir() {
			t.Fatalf("%s is not a directory (mode %v)", rel, fi.Mode())
		}
	}
}

// tarEntry は検体の tar に入れる 1 エントリ。ゼロ値は 0644 の通常ファイルになる。
type tarEntry struct {
	name     string
	typeflag byte
	mode     int64
	body     string
	linkname string
}

// buildTar は検体の tar を組み立てる。攻撃的なエントリもここで自由に作れるので、
// ネットワークから実物を落としてくる必要がない。
func buildTar(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{Name: e.name, Typeflag: typeflag, Mode: mode, Linkname: e.linkname}
		if typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if typeflag == tar.TypeXGlobalHeader {
			// グローバルヘッダは pax 書式にしか存在しない。
			hdr.Format, hdr.Mode = tar.FormatPAX, 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write tar header for %q: %v", e.name, err)
		}
		if hdr.Size > 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write tar body for %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zipEntry は検体の zip に入れる 1 エントリ。ゼロ値は 0644 の通常ファイルになる。
type zipEntry struct {
	name string
	body string
	mode fs.FileMode
	// declaredSize は中央ディレクトリに書く展開後サイズの自己申告値。
	// 0 以外なら実体と食い違う値を書き、申告を信じる実装だけが騙される検体になる。
	declaredSize uint64
}

// buildZip は検体の zip を組み立てる。
func buildZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Store}
		hdr.SetMode(mode)

		if e.declaredSize != 0 {
			// zip.Writer に書かせるとサイズは必ず実体と一致してしまう。
			// 詐称した検体を作るには、サイズを自分で決められる CreateRaw が要る。
			hdr.CRC32 = crc32.ChecksumIEEE([]byte(e.body))
			hdr.CompressedSize64 = uint64(len(e.body))
			hdr.UncompressedSize64 = e.declaredSize
			w, err := zw.CreateRaw(hdr)
			if err != nil {
				t.Fatalf("create raw zip entry %q: %v", e.name, err)
			}
			if _, err := w.Write([]byte(e.body)); err != nil {
				t.Fatalf("write zip body for %q: %v", e.name, err)
			}
			continue
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("create zip entry %q: %v", e.name, err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatalf("write zip body for %q: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// gzipBytes は tar を gzip でくるむ。
func gzipBytes(t *testing.T, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// writeSample は組み立てた検体をファイルへ書き出し、その位置を返す。
// 展開先とは別のディレクトリへ置く。脱出の検証では展開先の親に何も無いことを見るため。
func writeSample(t *testing.T, name string, body []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// newDest は空の展開先とその親を返す。
// 親を分けて持つのは、".." 付きのエントリが着地する先を見張るため。
func newDest(t *testing.T) (parent, dest string) {
	t.Helper()
	parent = t.TempDir()
	dest = filepath.Join(parent, "dest")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	return parent, dest
}

// assertNothingExtracted は展開先とその親のどちらにも何も生まれていないことを確かめる。
func assertNothingExtracted(t *testing.T, parent, dest string) {
	t.Helper()
	var found []string
	err := filepath.WalkDir(parent, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// 展開先そのものは呼び出し側が用意したもの。消さずに残っているのが正しい。
		if p == parent || p == dest {
			return nil
		}
		rel, err := filepath.Rel(parent, p)
		if err != nil {
			return err
		}
		found = append(found, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("entries created under %s = %v, want none", parent, found)
	}
}

// assertFile は展開されたファイルの内容と実行ビットを確かめる。
func assertFile(t *testing.T, p, want string, wantExec bool) {
	t.Helper()
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	if string(body) != want {
		t.Fatalf("content of %s = %q, want %q", p, body, want)
	}
	if runtime.GOOS == "windows" {
		// Windows のファイルモードに実行ビットは無い。
		return
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	if got := fi.Mode().Perm()&0o111 != 0; got != wantExec {
		t.Fatalf("executable bit of %s = %v (mode %v), want %v", p, got, fi.Mode().Perm(), wantExec)
	}
}
