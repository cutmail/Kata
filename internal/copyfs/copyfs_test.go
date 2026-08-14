package copyfs

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/cutmail/kata/internal/digest"
)

func TestDirCopiesTree(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	mustMkdir(t, filepath.Join(src, "nested", "deep"))
	mustWrite(t, filepath.Join(src, "SKILL.md"), "body", 0o644)
	mustWrite(t, filepath.Join(src, "nested", "deep", "note.txt"), "deep", 0o644)
	// 実行ビットの保存は skill 同梱スクリプトのために必須。
	mustWrite(t, filepath.Join(src, "scripts", "run.sh"), "#!/bin/sh\n", 0o755)

	dst := filepath.Join(root, "out", "skill")
	rep, err := Dir(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Skipped) != 0 {
		t.Fatalf("skipped = %v, want none", rep.Skipped)
	}

	assertFile(t, filepath.Join(dst, "SKILL.md"), "body", false)
	assertFile(t, filepath.Join(dst, "nested", "deep", "note.txt"), "deep", false)
	assertFile(t, filepath.Join(dst, "scripts", "run.sh"), "#!/bin/sh\n", true)

	// 複製元は読むだけで、書き換えてはならない。
	assertFile(t, filepath.Join(src, "scripts", "run.sh"), "#!/bin/sh\n", true)
}

func TestDirSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	mustMkdir(t, filepath.Join(src, "nested"))
	mustWrite(t, filepath.Join(src, "SKILL.md"), "body", 0o644)

	// ~/.claude にはこのマシンでしか意味を持たない絶対 symlink が混ざりうる。
	outside := filepath.Join(root, "outside")
	mustWrite(t, outside, "elsewhere", 0o644)
	if err := os.Symlink(outside, filepath.Join(src, "ref")); err != nil {
		t.Skipf("symlinks are not available in this environment: %v", err)
	}
	// 壊れたリンクも同じ扱いになること。
	if err := os.Symlink(filepath.Join(root, "gone"), filepath.Join(src, "nested", "dangling")); err != nil {
		t.Skipf("symlinks are not available in this environment: %v", err)
	}

	dst := filepath.Join(root, "dst")
	rep, err := Dir(src, dst)
	if err != nil {
		t.Fatal(err)
	}

	// os.CopyFS との決定的な違い: リンクを再現してはならない。
	for _, rel := range []string{"ref", filepath.Join("nested", "dangling")} {
		if _, err := os.Lstat(filepath.Join(dst, rel)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("lstat %s in destination: err = %v, want fs.ErrNotExist", rel, err)
		}
		assertSkipped(t, rep, rel, "symlink")
	}
	if rep.Files != 1 {
		t.Fatalf("files = %d, want 1", rep.Files)
	}
	assertFile(t, filepath.Join(dst, "SKILL.md"), "body", false)
}

func TestDirSkipsIrregularFiles(t *testing.T) {
	// syscall.Mkfifo は Windows に無く、この 1 件のためにビルドタグ付きの
	// ファイルを増やしたくないので mkfifo(1) を借りる。
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skipf("mkfifo is not available in this environment: %v", err)
	}
	root := t.TempDir()
	src := filepath.Join(root, "src")
	mustMkdir(t, src)
	mustWrite(t, filepath.Join(src, "SKILL.md"), "body", 0o644)
	if out, err := exec.Command(mkfifo, filepath.Join(src, "pipe")).CombinedOutput(); err != nil {
		t.Skipf("cannot create a fifo in this environment: %v: %s", err, out)
	}

	dst := filepath.Join(root, "dst")
	rep, err := Dir(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "pipe")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("lstat pipe in destination: err = %v, want fs.ErrNotExist", err)
	}
	assertSkipped(t, rep, "pipe", "irregular file")
	if rep.Files != 1 {
		t.Fatalf("files = %d, want 1", rep.Files)
	}
}

func TestDirRefusesExistingDestination(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	mustMkdir(t, src)
	mustWrite(t, filepath.Join(src, "SKILL.md"), "new", 0o644)

	dst := filepath.Join(root, "dst")
	mustMkdir(t, dst)
	mustWrite(t, filepath.Join(dst, "SKILL.md"), "existing", 0o644)

	if _, err := Dir(src, dst); err == nil {
		t.Fatal("copying onto an existing destination must fail")
	}
	// 利用者のファイルは手つかずのまま残っていなければならない。
	assertFile(t, filepath.Join(dst, "SKILL.md"), "existing", false)
	entries, err := os.ReadDir(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries in destination = %d, want 1", len(entries))
	}
}

func TestDirLeavesNothingOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not block reads on this platform")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can read a directory regardless of its mode")
	}

	root := t.TempDir()
	src := filepath.Join(root, "src")
	mustWrite(t, filepath.Join(src, "SKILL.md"), "body", 0o644)
	locked := filepath.Join(src, "locked")
	mustWrite(t, filepath.Join(locked, "inner.txt"), "inner", 0o644)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	// 読めないままだと t.TempDir の後片付けが失敗する。
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	dst := filepath.Join(root, "dst")
	if _, err := Dir(src, dst); err == nil {
		t.Fatal("copying an unreadable tree must fail")
	}
	if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("lstat destination: err = %v, want fs.ErrNotExist", err)
	}
	// 一時ディレクトリの残骸も残してはならない。
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "src" {
			t.Fatalf("leftover in the destination's parent: got %q, want only \"src\"", e.Name())
		}
	}
}

func TestDirEnforcesLimits(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	mustMkdir(t, src)
	for _, name := range []string{"a", "b", "c"} {
		mustWrite(t, filepath.Join(src, name), "xxxx", 0o644)
	}

	// 本番の上限は node_modules を取り込む事故を止めるためのもので、テストで
	// 20000 ファイルを作るのは現実的でない。同じ経路を小さい上限で通す。
	if _, err := copyDir(src, filepath.Join(root, "byfiles"), limits{files: 2, bytes: 1 << 20}); err == nil {
		t.Fatal("copying more files than the limit must fail")
	}
	if _, err := copyDir(src, filepath.Join(root, "bybytes"), limits{files: 100, bytes: 8}); err == nil {
		t.Fatal("copying more bytes than the limit must fail")
	}
	// 打ち切ったときに複製先を残さないのは通常の失敗と同じ。
	for _, name := range []string{"byfiles", "bybytes"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("lstat %s: err = %v, want fs.ErrNotExist", name, err)
		}
	}

	// 既定の上限は現実の skill を通すだけの余裕があること。
	if defaultLimits.files < 1000 || defaultLimits.bytes < 1<<20 {
		t.Fatalf("defaultLimits = %+v, want room for a realistic skill tree", defaultLimits)
	}
	if _, err := copyDir(src, filepath.Join(root, "ok"), defaultLimits); err != nil {
		t.Fatal(err)
	}
}

func TestDirPreservesEmptyDirectories(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	mustMkdir(t, filepath.Join(src, "empty"))
	mustMkdir(t, filepath.Join(src, "nested", "also-empty"))
	mustWrite(t, filepath.Join(src, "SKILL.md"), "body", 0o644)

	dst := filepath.Join(root, "dst")
	if _, err := Dir(src, dst); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"empty", filepath.Join("nested", "also-empty")} {
		fi, err := os.Stat(filepath.Join(dst, rel))
		if err != nil {
			t.Fatalf("stat %s in destination: %v", rel, err)
		}
		if !fi.IsDir() {
			t.Fatalf("%s in destination is not a directory", rel)
		}
		entries, err := os.ReadDir(filepath.Join(dst, rel))
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("entries in %s = %d, want 0", rel, len(entries))
		}
	}
}

func TestFileCopies(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "run.sh")
	mustWrite(t, src, "#!/bin/sh\n", 0o755)

	// 親ディレクトリが無くても複製できること。
	dst := filepath.Join(root, "out", "run.sh")
	rep, err := File(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	assertFile(t, dst, "#!/bin/sh\n", true)
	if rep.Files != 1 || rep.Bytes != int64(len("#!/bin/sh\n")) {
		t.Fatalf("report = %+v, want Files=1 Bytes=%d", rep, len("#!/bin/sh\n"))
	}

	// 既存の複製先は上書きしない。
	other := filepath.Join(root, "other.md")
	mustWrite(t, other, "other", 0o644)
	if _, err := File(other, dst); err == nil {
		t.Fatal("copying onto an existing file must fail")
	}
	assertFile(t, dst, "#!/bin/sh\n", true)

	// 実行ビットが無ければ引き継がない。
	plain := filepath.Join(root, "out", "other.md")
	if _, err := File(other, plain); err != nil {
		t.Fatal(err)
	}
	assertFile(t, plain, "other", false)

	// 通常ファイル以外は受け付けない。
	if _, err := File(filepath.Join(root, "out"), filepath.Join(root, "copied-dir")); err == nil {
		t.Fatal("copying a directory with File must fail")
	}

	// 一時ディレクトリの残骸を残さないこと。
	entries, err := os.ReadDir(filepath.Join(root, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries in destination directory = %d, want 2", len(entries))
	}
}

func TestReportCounts(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	mustMkdir(t, filepath.Join(src, "empty"))
	mustWrite(t, filepath.Join(src, "a.txt"), "12345", 0o644)
	mustWrite(t, filepath.Join(src, "sub", "b.txt"), "678", 0o644)
	// ディレクトリと symlink は Files にも Bytes にも数えない。
	hasLink := os.Symlink(filepath.Join(src, "a.txt"), filepath.Join(src, "link")) == nil

	rep, err := Dir(src, filepath.Join(root, "dst"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Files != 2 {
		t.Fatalf("files = %d, want 2", rep.Files)
	}
	if rep.Bytes != 8 {
		t.Fatalf("bytes = %d, want 8", rep.Bytes)
	}
	wantSkipped := 0
	if hasLink {
		wantSkipped = 1
	}
	if len(rep.Skipped) != wantSkipped {
		t.Fatalf("skipped = %v, want %d entries", rep.Skipped, wantSkipped)
	}
}

// TestDirOutputMatchesDigest は、複製元と複製先の内容ダイジェストが一致することを
// 確かめる。copy 戦略はこの一致を前提に「配置後に編集されていないか」を判定するため、
// 権限の正規化規則が digest とずれると、配置した直後に編集済みと誤判定される。
func TestDirOutputMatchesDigest(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	mustMkdir(t, filepath.Join(src, "assets"))
	// 取り込み元の権限はまちまちでありうる。正規化されて揃うことが要点。
	mustWrite(t, filepath.Join(src, "SKILL.md"), "hello\n", 0o600)
	mustWrite(t, filepath.Join(src, "bin", "run.sh"), "#!/bin/sh\n", 0o777)

	dst := filepath.Join(root, "out", "skill")
	if _, err := Dir(src, dst); err != nil {
		t.Fatal(err)
	}

	want, err := digest.Tree(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := digest.Tree(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("digest of the copy = %s, want %s (the copy must be indistinguishable)", got, want)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// mustWrite は親ディレクトリごとファイルを作る。
// 権限は umask に左右されないよう明示的に付け直す。
func mustWrite(t *testing.T, p, body string, mode fs.FileMode) {
	t.Helper()
	mustMkdir(t, filepath.Dir(p))
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, p, want string, wantExec bool) {
	t.Helper()
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	if string(body) != want {
		t.Fatalf("content of %s = %q, want %q", p, body, want)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %s: %v", p, err)
	}
	if got := fi.Mode().Perm()&0o111 != 0; got != wantExec {
		t.Fatalf("executable bit of %s = %v (mode %v), want %v", p, got, fi.Mode().Perm(), wantExec)
	}
}

func assertSkipped(t *testing.T, rep Report, path, reason string) {
	t.Helper()
	for _, s := range rep.Skipped {
		if s.Path != path {
			continue
		}
		if s.Reason != reason {
			t.Fatalf("skip reason for %s = %q, want %q", path, s.Reason, reason)
		}
		return
	}
	t.Fatalf("skipped = %v, want an entry for %s", rep.Skipped, path)
}
