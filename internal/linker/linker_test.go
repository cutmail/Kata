package linker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyCreatesLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "src")
	mustMkdir(t, target)
	dest := filepath.Join(dir, "out", "link")

	res, err := Apply(dest, target, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if res != Created {
		t.Fatalf("result = %v, want Created", res)
	}
	got, err := os.Readlink(dest)
	if err != nil || got != target {
		t.Fatalf("readlink = %q, %v", got, err)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "src")
	mustMkdir(t, target)
	dest := filepath.Join(dir, "link")

	if _, err := Apply(dest, target, false, ""); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(dest, target, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if res != Unchanged {
		t.Fatalf("result = %v, want Unchanged", res)
	}
}

func TestApplySwapsExistingLink(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	mustMkdir(t, a)
	mustMkdir(t, b)
	dest := filepath.Join(dir, "link")

	if _, err := Apply(dest, a, false, ""); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(dest, b, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if res != Updated {
		t.Fatalf("result = %v, want Updated", res)
	}
	if got, _ := os.Readlink(dest); got != b {
		t.Fatalf("readlink = %q, want %q", got, b)
	}
}

func TestApplyRefusesToOverwriteRealFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "src")
	mustMkdir(t, target)
	dest := filepath.Join(dir, "existing")
	if err := os.WriteFile(dest, []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Apply(dest, target, false, ""); !errors.Is(err, ErrOccupied) {
		t.Fatalf("err = %v, want ErrOccupied", err)
	}
	// 利用者のファイルは手つかずのまま残っていなければならない。
	body, err := os.ReadFile(dest)
	if err != nil || string(body) != "user data" {
		t.Fatalf("existing file was touched: %q, %v", body, err)
	}
}

func TestApplyForceBacksUpExistingFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "src")
	mustMkdir(t, target)
	dest := filepath.Join(dir, "existing")
	if err := os.WriteFile(dest, []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}
	backups := filepath.Join(dir, "backups")

	res, err := Apply(dest, target, true, backups)
	if err != nil {
		t.Fatal(err)
	}
	if res != Updated {
		t.Fatalf("result = %v, want Updated", res)
	}
	if got, _ := os.Readlink(dest); got != target {
		t.Fatalf("readlink = %q", got)
	}
	if !findFile(t, backups, "existing") {
		t.Fatal("original file was not backed up")
	}
}

// TestApplyForceKeepsEveryBackup は、同名の実体を続けて退避しても
// 先に退避したものが失われないことを確かめる。退避は「削除はしない」という
// 保証の実装なので、rename による黙った上書きが起きてはならない。
func TestApplyForceKeepsEveryBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "src")
	mustMkdir(t, target)
	backups := filepath.Join(dir, "backups")
	stamp := NewStamp()

	// 別ディレクトリにある同名の実体を、同じ退避先へ 2 回どける。
	for i, body := range []string{"first", "second"} {
		sub := filepath.Join(dir, fmt.Sprintf("d%d", i))
		mustMkdir(t, sub)
		dest := filepath.Join(sub, "same-name")
		if err := os.WriteFile(dest, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		opts := Options{Force: true, BackupDir: backups, Stamp: stamp}
		if _, err := ApplyWith(dest, target, opts); err != nil {
			t.Fatal(err)
		}
	}

	// 退避先に 2 件とも残っていること。
	got := map[string]bool{}
	_ = filepath.Walk(backups, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			body, rerr := os.ReadFile(p)
			if rerr == nil {
				got[string(body)] = true
			}
		}
		return nil
	})
	if !got["first"] || !got["second"] {
		t.Fatalf("backed up contents = %v, want both first and second", got)
	}

	// Stamp を渡したので退避先は 1 つのディレクトリにまとまる。
	entries, err := os.ReadDir(backups)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != stamp {
		t.Fatalf("backup dirs = %v, want a single %q", entries, stamp)
	}
}

func TestRemoveOnlyDeletesManagedLinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "src")
	mustMkdir(t, target)

	link := filepath.Join(dir, "link")
	if _, err := Apply(link, target, false, ""); err != nil {
		t.Fatal(err)
	}
	removed, err := Remove(link, target)
	if err != nil || !removed {
		t.Fatalf("removed = %v, err = %v", removed, err)
	}

	// 実ファイルは対象外。
	real := filepath.Join(dir, "real")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	removed, err = Remove(real, target)
	if err != nil || removed {
		t.Fatalf("removed = %v, err = %v; real files must be left alone", removed, err)
	}
	if _, err := os.Stat(real); err != nil {
		t.Fatal("real file was deleted")
	}

	// 別の場所を指すリンクも対象外。
	other := filepath.Join(dir, "other")
	mustMkdir(t, other)
	foreign := filepath.Join(dir, "foreign")
	if err := os.Symlink(other, foreign); err != nil {
		t.Fatal(err)
	}
	removed, _ = Remove(foreign, target)
	if removed {
		t.Fatal("a link pointing elsewhere must not be removed")
	}

	// 存在しないパスは黙って何もしない。
	removed, err = Remove(filepath.Join(dir, "absent"), target)
	if err != nil || removed {
		t.Fatalf("removed = %v, err = %v", removed, err)
	}
}

func TestIsBrokenLink(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken")
	if err := os.Symlink(filepath.Join(dir, "gone"), broken); err != nil {
		t.Fatal(err)
	}
	if !IsBrokenLink(broken) {
		t.Fatal("expected a broken link to be detected")
	}

	target := filepath.Join(dir, "src")
	mustMkdir(t, target)
	ok := filepath.Join(dir, "ok")
	if err := os.Symlink(target, ok); err != nil {
		t.Fatal(err)
	}
	if IsBrokenLink(ok) {
		t.Fatal("a healthy link must not be reported as broken")
	}
	if IsBrokenLink(target) {
		t.Fatal("a real directory must not be reported as broken")
	}
}

func TestSymlinkSupported(t *testing.T) {
	if !SymlinkSupported(filepath.Join(t.TempDir(), "probe")) {
		t.Skip("symlinks are not available in this environment")
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func findFile(t *testing.T, root, name string) bool {
	t.Helper()
	found := false
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && info.Name() == name {
			found = true
		}
		return nil
	})
	return found
}
