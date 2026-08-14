package safepath

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// mustSymlink は symlink を作る。作れない環境ではテストを飛ばす。
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are not available in this environment: %v", err)
	}
}

// TestVerifyUnderRejectsSymlinkComponent は、経路の途中の symlink を拒否することを
// 確かめる。字句的には内側に見えるパスが、実際には外を指す典型。
func TestVerifyUnderRejectsSymlinkComponent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	base := filepath.Join(root, "base")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, outside, filepath.Join(base, "hop"))

	err := VerifyUnder(base, filepath.Join(base, "hop", "skills"))
	if !errors.Is(err, ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}
}

// TestVerifyUnderAllowsMissingComponents は、まだ無い要素で止めることを確かめる。
// 配置先は kata がこれから作るものなので、存在しないことは異常ではない。
func TestVerifyUnderAllowsMissingComponents(t *testing.T) {
	base := t.TempDir()
	if err := VerifyUnder(base, filepath.Join(base, "skills", "not-yet")); err != nil {
		t.Fatalf("err = %v, want nil for a path that does not exist yet", err)
	}
}

// TestVerifyUnderRejectsEscape は、字句的に外へ出る指定を拒否することを確かめる。
func TestVerifyUnderRejectsEscape(t *testing.T) {
	base := t.TempDir()
	if err := VerifyUnder(base, filepath.Join(base, "..", "elsewhere")); !errors.Is(err, ErrEscapes) {
		t.Fatalf("err = %v, want ErrEscapes", err)
	}
}

// TestVerifyUnderIgnoresBaseItself は、base 自身の symlink は見ないことを確かめる。
// 利用者が ~/.claude を別の場所へ張るのは正当な運用。
func TestVerifyUnderIgnoresBaseItself(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "link")
	mustSymlink(t, real, base)

	if err := VerifyUnder(base, filepath.Join(base, "skills")); err != nil {
		t.Fatalf("err = %v, want nil; a symlinked base is the user's own choice", err)
	}
}

// TestVerifyTreeRejectsSymlink は、ツリーに含まれる symlink を拒否することを確かめる。
// 配置してしまうと、取得元のリンク越しにホームの任意の場所が読めてしまう。
func TestVerifyTreeRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, "/etc", filepath.Join(tree, "nested", "ref"))

	if err := VerifyTree(tree); !errors.Is(err, ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}
}

// TestVerifyTreeRejectsSymlinkRoot は、ルート自体が symlink でも拒否することを確かめる。
func TestVerifyTreeRejectsSymlinkRoot(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "link")
	mustSymlink(t, "/etc", link)

	if err := VerifyTree(link); !errors.Is(err, ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}
}

// TestVerifyTreeAcceptsPlainTree は、通常のツリーを通すことを確かめる。
func TestVerifyTreeAcceptsPlainTree(t *testing.T) {
	tree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "bin", "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := VerifyTree(tree); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

// TestVerifyTreeAcceptsSingleFile は、単一ファイルも扱えることを確かめる。
func TestVerifyTreeAcceptsSingleFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pr.md")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyTree(p); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
}

// TestCheckTreeSizeEnforcesLimits は、上限を超えるツリーを拒否することを確かめる。
func TestCheckTreeSizeEnforcesLimits(t *testing.T) {
	tree := t.TempDir()
	for i := range 5 {
		p := filepath.Join(tree, string(rune('a'+i))+".txt")
		if err := os.WriteFile(p, []byte("0123456789"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckTreeSize(tree, TreeLimits{Files: 3, Bytes: 1 << 20}); err == nil {
		t.Fatal("expected the file count limit to be enforced")
	}
	if err := CheckTreeSize(tree, TreeLimits{Files: 100, Bytes: 20}); err == nil {
		t.Fatal("expected the byte limit to be enforced")
	}
	if err := CheckTreeSize(tree, TreeLimits{Files: 100, Bytes: 1 << 20}); err != nil {
		t.Fatalf("err = %v, want nil for a tree within the limits", err)
	}
}

// TestNotSymlink は 1 点だけの確認を扱えることを確かめる。
func TestNotSymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := NotSymlink(real); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	// 無いものは「symlink ではない」として通す。
	if err := NotSymlink(filepath.Join(root, "absent")); err != nil {
		t.Fatalf("err = %v, want nil for a missing path", err)
	}
	link := filepath.Join(root, "link")
	mustSymlink(t, real, link)
	if err := NotSymlink(link); !errors.Is(err, ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}
}
