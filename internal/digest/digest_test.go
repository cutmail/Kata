package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTreeIgnoresMtimeAndOwner(t *testing.T) {
	// 同じ内容のツリーを 2 つ作り、片方だけ時刻をずらす。
	// mtime はコピーのたびに変わるため、digest に混ざると copy 戦略の所有証明が成り立たない。
	a := t.TempDir()
	mkFile(t, a, "SKILL.md", "body", false)
	mkFile(t, a, "assets/logo.txt", "logo", false)

	b := t.TempDir()
	mkFile(t, b, "SKILL.md", "body", false)
	mkFile(t, b, "assets/logo.txt", "logo", false)

	old := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, rel := range []string{"SKILL.md", "assets/logo.txt", "assets"} {
		if err := os.Chtimes(filepath.Join(b, filepath.FromSlash(rel)), old, old); err != nil {
			t.Fatal(err)
		}
	}

	if got, want := mustTree(t, b), mustTree(t, a); got != want {
		t.Errorf("digest = %s, want %s; mtime must not change the digest", got, want)
	}
}

func TestTreeDetectsContentAndShapeChanges(t *testing.T) {
	// 利用者の編集を見逃すと、copy 戦略が「kata が置いたまま」と誤判定して消してしまう。
	// 内容だけでなく、ツリーの形の変化も必ず digest に出ること。
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string)
	}{
		{"edited content", func(t *testing.T, root string) {
			mkFile(t, root, "SKILL.md", "edited", false)
		}},
		{"added file", func(t *testing.T, root string) {
			mkFile(t, root, "extra.md", "", false)
		}},
		{"added empty dir", func(t *testing.T, root string) {
			mkDir(t, filepath.Join(root, "empty"))
		}},
		{"renamed file", func(t *testing.T, root string) {
			if err := os.Rename(filepath.Join(root, "SKILL.md"), filepath.Join(root, "RENAMED.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"added exec bit", func(t *testing.T, root string) {
			if err := os.Chmod(filepath.Join(root, "SKILL.md"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// NTFS に実行ビットの概念がなく、chmod は digest に影響しない。
			if tt.name == "added exec bit" && runtime.GOOS == "windows" {
				t.Skip("exec bit has no meaning on this platform")
			}
			root := mkBaseTree(t)
			before := mustTree(t, root)
			tt.mutate(t, root)
			if after := mustTree(t, root); after == before {
				t.Errorf("digest = %s, want a different value; %s went undetected", after, tt.name)
			}
		})
	}
}

func TestTreeIsOrderIndependentOfWalk(t *testing.T) {
	// WalkDir は親ディレクトリ内の名前順にしか訪問しないので、"a" < "a.txt" より
	// 訪問順は a, a/b, a.txt になる。一方フルパスのバイト順は a, a.txt, a/b（'.' < '/'）。
	// 訪問順のまま連結する実装を検出するため、期待値を手で組み立てて突き合わせる。
	root := t.TempDir()
	mkFile(t, root, "a.txt", "one", false)
	mkFile(t, root, "a/b", "two", false)

	sum := sha256.Sum256([]byte(strings.Join([]string{
		TreeVersion + "\x00",
		"d\x00" + "a" + "\x00",
		"f\x00" + "a.txt" + "\x00" + "644" + "\x00" + "3" + "\x00" + "one",
		"f\x00" + "a/b" + "\x00" + "644" + "\x00" + "3" + "\x00" + "two",
	}, "")))
	want := Prefix + hex.EncodeToString(sum[:])
	if got := mustTree(t, root); got != want {
		t.Errorf("digest = %s, want %s; entries must be sorted by full relative path", got, want)
	}

	// 作成順が違うだけの等価なツリーは同じ値になること。
	other := t.TempDir()
	mkFile(t, other, "a/b", "two", false)
	mkFile(t, other, "a.txt", "one", false)
	if got := mustTree(t, other); got != want {
		t.Errorf("digest = %s, want %s; equivalent trees must agree", got, want)
	}
}

func TestTreeGoldenValue(t *testing.T) {
	// state.json に記録済みの digest と突き合わせる以上、アルゴリズムが黙って変わると
	// 「利用者が編集した」と全件誤判定する。値を固定して、意図しない変更を検知する。
	// 意図して書式を変えるときは TreeVersion を上げたうえでこの定数を更新すること。
	//
	// 期待値は実行ビット付きファイルを含む。NTFS には実行ビットの概念がなく
	// 値が一致しないため、この環境では検証できない。
	if runtime.GOOS == "windows" {
		t.Skip("golden value depends on the exec bit, which has no meaning on this platform")
	}
	root := t.TempDir()
	mkFile(t, root, "SKILL.md", "hello\n", false)
	mkFile(t, root, "bin/run.sh", "#!/bin/sh\n", true)
	mkDir(t, filepath.Join(root, "assets"))

	// プリイメージ（エントリはバイト順: SKILL.md, assets, bin, bin/run.sh）:
	//   "kata-tree-v1\x00"
	//   "f\x00SKILL.md\x00644\x006\x00hello\n"
	//   "d\x00assets\x00"
	//   "d\x00bin\x00"
	//   "f\x00bin/run.sh\x00755\x0010\x00#!/bin/sh\n"
	const want = "sha256:42ce6ae1896f7ae950beed139be30d9c0ef8cd3912d2df8a67269b0d01284436"

	if got := mustTree(t, root); got != want {
		t.Fatalf("digest = %s, want %s; the tree preimage format changed", got, want)
	}
}

func TestTreeRejectsSymlink(t *testing.T) {
	// copy 戦略は symlink を配置しない。digest 側も同じ定義でなければ、
	// 「配置したはずのもの」と「digest が対象にするもの」がずれる。
	root := t.TempDir()
	mkFile(t, root, "SKILL.md", "body", false)
	if err := os.Symlink(filepath.Join(root, "SKILL.md"), filepath.Join(root, "link.md")); err != nil {
		t.Skip("symlinks are not available in this environment")
	}
	if _, err := Tree(root); !errors.Is(err, ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}

	// root 自身が symlink の場合も同じ扱い。
	plain := t.TempDir()
	mkFile(t, plain, "SKILL.md", "body", false)
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(plain, link); err != nil {
		t.Skip("symlinks are not available in this environment")
	}
	if _, err := Tree(link); !errors.Is(err, ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink for a symlinked root", err)
	}
}

func TestTreeSingleFile(t *testing.T) {
	dir := t.TempDir()
	mkFile(t, dir, "a.md", "same body", false)
	mkFile(t, dir, "b.md", "same body", false)
	mkFile(t, dir, "c.md", "other body", false)

	a := mustTree(t, filepath.Join(dir, "a.md"))
	// 単一ファイルは rel = "" の 1 エントリ。ソース側の名前と配置先の名前は独立なので、
	// ファイル名が変わっても同じ値でなければならない。
	if b := mustTree(t, filepath.Join(dir, "b.md")); b != a {
		t.Errorf("digest = %s, want %s; the file name must not affect the digest", b, a)
	}
	if c := mustTree(t, filepath.Join(dir, "c.md")); c == a {
		t.Errorf("digest = %s, want a different value; the content differs", c)
	}
}

func TestSum(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"abc", "abc", "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Sum(strings.NewReader(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("Sum(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestEqual(t *testing.T) {
	const d = "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		// 「記録が無い」を一致と扱うと、覚えのない実体を撤去してしまう。
		{"both empty", "", "", false},
		{"empty left", "", d, false},
		{"empty right", d, "", false},
		{"same", d, d, true},
		{"different", d, "sha256:00", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Equal(tt.a, tt.b); got != tt.want {
				t.Errorf("Equal(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestSanitizeMode(t *testing.T) {
	tests := []struct {
		name string
		in   fs.FileMode
		want fs.FileMode
	}{
		{"world writable executable", 0o777, 0o755},
		{"owner only executable", 0o700, 0o755},
		{"private file", 0o600, 0o644},
		{"regular file", 0o644, 0o644},
		{"setuid is dropped", fs.ModeSetuid | 0o755, 0o755},
		{"setuid without exec bits is dropped", fs.ModeSetuid | 0o600, 0o644},
		{"setgid and sticky are dropped", fs.ModeSetgid | fs.ModeSticky | 0o644, 0o644},
		{"dir is always 0755", fs.ModeDir | 0o700, 0o755},
		{"dir without exec bits is 0755", fs.ModeDir | 0o600, 0o755},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeMode(tt.in); got != tt.want {
				t.Errorf("SanitizeMode(%04o) = %04o, want %04o", tt.in, got, tt.want)
			}
		})
	}
}

// mkBaseTree は変化検出テストの土台になるツリーを作り、その root を返す。
func mkBaseTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mkFile(t, root, "SKILL.md", "body", false)
	mkFile(t, root, "assets/logo.txt", "logo", false)
	return root
}

// mkFile は root からの相対パスにファイルを作る。親ディレクトリも一緒に作る。
// umask に左右されないよう、書き込んだあとに必ず Chmod する。
func mkFile(t *testing.T, root, rel, body string, exec bool) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	mkDir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mode := fs.FileMode(0o644)
	if exec {
		mode = 0o755
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// mkDir はディレクトリを作る。
func mkDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// mustTree は Tree を呼び、失敗したらその場で終わる。
func mustTree(t *testing.T, root string) string {
	t.Helper()
	got, err := Tree(root)
	if err != nil {
		t.Fatalf("Tree(%s) failed: %v", root, err)
	}
	return got
}
