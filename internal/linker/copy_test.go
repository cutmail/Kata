package linker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cutmail/kata/internal/digest"
)

// mkTree は複製元になるディレクトリを作って絶対パスを返す。
func mkTree(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// mustDigest は木の内容ダイジェストを返す。
func mustDigest(t *testing.T, p string) string {
	t.Helper()
	d, err := digest.Tree(p)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// assertBody は配置先のファイルの中身を確かめる。
func assertBody(t *testing.T, p, want string) {
	t.Helper()
	got, err := os.ReadFile(p)
	if err != nil || string(got) != want {
		t.Fatalf("content of %s = %q, %v; want %q", p, got, err, want)
	}
}

func TestApplyCopyCreatesRealFiles(t *testing.T) {
	root := t.TempDir()
	src := mkTree(t, root, "src", "hello\n")
	dest := filepath.Join(root, "out", "skill")

	res, d, err := ApplyCopy(CopyRequest{Dest: dest, Src: src})
	if err != nil || res != Created {
		t.Fatalf("result = %v, %v; want Created", res, err)
	}
	// symlink ではなく実体であること。
	fi, err := os.Lstat(dest)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("destination should be a real directory: %v, %v", fi, err)
	}
	assertBody(t, filepath.Join(dest, "SKILL.md"), "hello\n")
	if d != mustDigest(t, src) {
		t.Fatalf("reported digest = %s, want the digest of the source", d)
	}
}

// TestApplyCopyIsIdempotent は 2 回目が何もしないことを確かめる。
// 毎回書き直すと冪等性が崩れ、sync のたびに更新が報告される。
func TestApplyCopyIsIdempotent(t *testing.T) {
	root := t.TempDir()
	src := mkTree(t, root, "src", "hello\n")
	dest := filepath.Join(root, "out", "skill")

	_, d1, err := ApplyCopy(CopyRequest{Dest: dest, Src: src})
	if err != nil {
		t.Fatal(err)
	}
	res, d2, err := ApplyCopy(CopyRequest{Dest: dest, Src: src, Known: d1})
	if err != nil || res != Unchanged {
		t.Fatalf("second apply = %v, %v; want Unchanged", res, err)
	}
	if d1 != d2 {
		t.Fatalf("digest changed between identical deployments: %s -> %s", d1, d2)
	}
}

// TestApplyCopyRefusesUnmanagedDestination は、kata が置いたのではない実体を
// 上書きしないことを確かめる。
func TestApplyCopyRefusesUnmanagedDestination(t *testing.T) {
	root := t.TempDir()
	src := mkTree(t, root, "src", "from kata\n")
	dest := mkTree(t, root, "dest", "user data\n")

	_, _, err := ApplyCopy(CopyRequest{Dest: dest, Src: src})
	if !errors.Is(err, ErrOccupied) {
		t.Fatalf("err = %v, want ErrOccupied", err)
	}
	// 利用者のファイルは手つかずのまま残っていなければならない。
	assertBody(t, filepath.Join(dest, "SKILL.md"), "user data\n")
}

// TestApplyCopyRefusesToOverwriteEditedCopy は、kata が置いたものでも
// 配置後に編集されていれば上書きしないことを確かめる。
func TestApplyCopyRefusesToOverwriteEditedCopy(t *testing.T) {
	root := t.TempDir()
	src := mkTree(t, root, "src", "v1\n")
	dest := filepath.Join(root, "out", "skill")

	_, known, err := ApplyCopy(CopyRequest{Dest: dest, Src: src})
	if err != nil {
		t.Fatal(err)
	}
	// 利用者が配置先を直接編集した。
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte("my edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 取得元も進んだ。
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err = ApplyCopy(CopyRequest{Dest: dest, Src: src, Known: known})
	if !errors.Is(err, ErrModified) {
		t.Fatalf("err = %v, want ErrModified", err)
	}
	// 編集が生き残っていること。
	assertBody(t, filepath.Join(dest, "SKILL.md"), "my edit\n")
}

// TestApplyCopyForceBacksUpBeforeReplacing は、--force が削除ではなく
// 退避であることを確かめる。
func TestApplyCopyForceBacksUpBeforeReplacing(t *testing.T) {
	root := t.TempDir()
	src := mkTree(t, root, "src", "from kata\n")
	dest := mkTree(t, root, "dest", "user data\n")
	backups := filepath.Join(root, "backups")

	res, _, err := ApplyCopy(CopyRequest{
		Dest: dest, Src: src, Force: true, BackupDir: backups, Stamp: "stamp",
	})
	if err != nil || res != Updated {
		t.Fatalf("result = %v, %v; want Updated", res, err)
	}
	assertBody(t, filepath.Join(dest, "SKILL.md"), "from kata\n")
	assertBody(t, filepath.Join(backups, "stamp", "dest", "SKILL.md"), "user data\n")
}

// TestApplyCopyRequiresAdoptForIdenticalContent は、内容が同じでも記録が無ければ
// 黙って管理下に取り込まないことを確かめる。
func TestApplyCopyRequiresAdoptForIdenticalContent(t *testing.T) {
	root := t.TempDir()
	src := mkTree(t, root, "src", "same\n")
	dest := mkTree(t, root, "dest", "same\n")

	_, _, err := ApplyCopy(CopyRequest{Dest: dest, Src: src})
	if !errors.Is(err, ErrOccupied) {
		t.Fatalf("err = %v, want ErrOccupied", err)
	}
	if !strings.Contains(err.Error(), "--adopt") {
		t.Fatalf("err = %v, want it to mention --adopt", err)
	}

	// --adopt を指定すると、ファイルには触れないまま引き取る。
	before, err := os.Stat(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	// 引き取りは Adopted として返る。ディスクは変わらないが、以後 kata が
	// 撤去できるようになるので、呼び出し側が黙って済ませないための区別。
	res, d, err := ApplyCopy(CopyRequest{Dest: dest, Src: src, Adopt: true})
	if err != nil || res != Adopted {
		t.Fatalf("adopt = %v, %v; want Adopted", res, err)
	}
	if d != mustDigest(t, src) {
		t.Fatalf("adopted digest = %s, want the digest of the source", d)
	}
	after, err := os.Stat(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	// 書き直していないこと。
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("adopting identical content must not rewrite the destination")
	}
}

// TestApplyCopyMigratesFromLink は link 戦略からの切り替えを確かめる。
func TestApplyCopyMigratesFromLink(t *testing.T) {
	root := t.TempDir()
	src := mkTree(t, root, "src", "hello\n")
	dest := filepath.Join(root, "out", "skill")

	if _, err := Apply(dest, src, false, ""); err != nil {
		t.Skipf("symlinks are not available in this environment: %v", err)
	}
	res, _, err := ApplyCopy(CopyRequest{Dest: dest, Src: src, LinkTarget: src})
	if err != nil || res != Updated {
		t.Fatalf("result = %v, %v; want Updated", res, err)
	}
	fi, err := os.Lstat(dest)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("the destination should have become a real directory")
	}
	assertBody(t, filepath.Join(dest, "SKILL.md"), "hello\n")
}

// TestApplyCopyRefusesForeignLink は、kata が張ったのではないリンクを
// 実体で置き換えないことを確かめる。
func TestApplyCopyRefusesForeignLink(t *testing.T) {
	root := t.TempDir()
	src := mkTree(t, root, "src", "hello\n")
	other := mkTree(t, root, "other", "elsewhere\n")
	dest := filepath.Join(root, "link")
	if err := os.Symlink(other, dest); err != nil {
		t.Skip("symlinks are not available in this environment")
	}

	_, _, err := ApplyCopy(CopyRequest{Dest: dest, Src: src, LinkTarget: "/somewhere/else"})
	if !errors.Is(err, ErrOccupied) {
		t.Fatalf("err = %v, want ErrOccupied", err)
	}
	if got, _ := os.Readlink(dest); got != other {
		t.Fatalf("the foreign link was disturbed: %q", got)
	}
}

// TestApplyCopyRejectsSourceWithSymlink は、複製元に symlink があれば配置しないことを
// 確かめる。判定に使うダイジェストが symlink を扱えない以上、配置もしてはならない。
func TestApplyCopyRejectsSourceWithSymlink(t *testing.T) {
	root := t.TempDir()
	src := mkTree(t, root, "src", "hello\n")
	if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(src, "ref")); err != nil {
		t.Skip("symlinks are not available in this environment")
	}
	dest := filepath.Join(root, "out", "skill")

	if _, _, err := ApplyCopy(CopyRequest{Dest: dest, Src: src}); err == nil {
		t.Fatal("expected a source containing a symlink to be rejected")
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatal("nothing should have been deployed")
	}
}

// TestApplyCopyLeavesNoTempResidue は、成功時も失敗時も作業用の一時ディレクトリが
// 残らないことを確かめる。
func TestApplyCopyLeavesNoTempResidue(t *testing.T) {
	root := t.TempDir()
	src := mkTree(t, root, "src", "hello\n")
	out := filepath.Join(root, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(out, "skill")

	if _, _, err := ApplyCopy(CopyRequest{Dest: dest, Src: src}); err != nil {
		t.Fatal(err)
	}
	// 上書きを拒まれる経路も通す。
	if _, _, err := ApplyCopy(CopyRequest{Dest: dest, Src: src}); err == nil {
		t.Fatal("expected the second apply without a record to be refused")
	}

	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".kata-") {
			t.Fatalf("temporary directory left behind: %s", e.Name())
		}
	}
}

// TestRemoveCopyLeavesEditedContent は、配置後に編集されたものを撤去しないことを
// 確かめる。os.RemoveAll は取り返しがつかないため、証明が取れないときは必ず残す。
func TestRemoveCopyLeavesEditedContent(t *testing.T) {
	root := t.TempDir()
	src := mkTree(t, root, "src", "v1\n")
	dest := filepath.Join(root, "out", "skill")

	_, known, err := ApplyCopy(CopyRequest{Dest: dest, Src: src})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte("my edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveCopy(dest, known)
	if err != nil || removed {
		t.Fatalf("removed = %v, err = %v; edited content must be left in place", removed, err)
	}
	assertBody(t, filepath.Join(dest, "SKILL.md"), "my edit\n")
}

// TestRemoveCopyIgnoresEmptyDigest は、記録が無いものを消さないことを確かめる。
func TestRemoveCopyIgnoresEmptyDigest(t *testing.T) {
	root := t.TempDir()
	dest := mkTree(t, root, "dest", "user data\n")

	removed, err := RemoveCopy(dest, "")
	if err != nil || removed {
		t.Fatalf("removed = %v, err = %v; an unproven destination must never be deleted", removed, err)
	}
	assertBody(t, filepath.Join(dest, "SKILL.md"), "user data\n")
}

// TestRemoveCopyDeletesOnlyItsOwnTree は、証明が取れたものだけを消し、
// 周りに手を出さないことを確かめる。
func TestRemoveCopyDeletesOnlyItsOwnTree(t *testing.T) {
	root := t.TempDir()
	src := mkTree(t, root, "src", "hello\n")
	out := filepath.Join(root, "out")
	dest := filepath.Join(out, "skill")

	_, known, err := ApplyCopy(CopyRequest{Dest: dest, Src: src})
	if err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(out, "sibling.md")
	if err := os.WriteFile(sibling, []byte("neighbour\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveCopy(dest, known)
	if err != nil || !removed {
		t.Fatalf("removed = %v, err = %v; want it removed", removed, err)
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatal("the deployed copy should be gone")
	}
	assertBody(t, sibling, "neighbour\n")
	// 複製元には決して手を出さない。
	assertBody(t, filepath.Join(src, "SKILL.md"), "hello\n")
}

// TestRemoveCopyIgnoresSymlinks は、link 戦略の配置を copy の経路で消さないことを
// 確かめる。
func TestRemoveCopyIgnoresSymlinks(t *testing.T) {
	root := t.TempDir()
	src := mkTree(t, root, "src", "hello\n")
	dest := filepath.Join(root, "link")
	if err := os.Symlink(src, dest); err != nil {
		t.Skip("symlinks are not available in this environment")
	}

	removed, err := RemoveCopy(dest, mustDigest(t, src))
	if err != nil || removed {
		t.Fatalf("removed = %v, err = %v; symlinks belong to the link strategy", removed, err)
	}
	if _, err := os.Lstat(dest); err != nil {
		t.Fatal("the link should still be there")
	}
}
