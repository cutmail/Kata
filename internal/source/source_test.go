package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cutmail/kata/internal/store"
)

func TestResolveSubpath(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "skills", "pdf")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := resolveSubpath(root, "skills/pdf")
	if err != nil || got != sub {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, err := resolveSubpath(root, ""); err != nil || got != root {
		t.Fatalf("empty subpath should return the root: %q, %v", got, err)
	}
	if _, err := resolveSubpath(root, "../escape"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected an escape error, got %v", err)
	}
	if _, err := resolveSubpath(root, "missing"); err == nil {
		t.Fatal("expected an error for a missing subpath")
	}
}

func TestLocalFetch(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "local", "skills", "a")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	l := NewLocal()
	got, err := l.Fetch(context.Background(), Request{Local: "./local/skills/a", BaseDir: base})
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != target {
		t.Fatalf("root = %q, want %q", got.Root, target)
	}
	if got.Commit != "" {
		t.Fatalf("local sources have no commit, got %q", got.Commit)
	}

	if _, err := l.Fetch(context.Background(), Request{Local: "../outside", BaseDir: base}); err == nil {
		t.Fatal("expected an escape error")
	}
	if _, err := l.Fetch(context.Background(), Request{Local: "./nope", BaseDir: base}); err == nil {
		t.Fatal("expected a not-found error")
	}
	if _, err := l.Fetch(context.Background(), Request{Local: "./x"}); err == nil {
		t.Fatal("expected an error when no base directory is given")
	}
}

// TestGitFetch はネットワークを使うため -short では実行しない。
func TestGitFetch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network test")
	}
	s := store.New(t.TempDir())
	g := NewGit(s)
	req := Request{
		Git:  "https://github.com/anthropics/skills",
		Ref:  "main",
		Path: "skills/pdf",
	}

	got, err := g.Fetch(context.Background(), req)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got.Commit == "" || len(got.Commit) != 40 {
		t.Fatalf("commit = %q", got.Commit)
	}
	if _, err := os.Stat(filepath.Join(got.Root, "SKILL.md")); err != nil {
		t.Fatalf("SKILL.md not found under %s: %v", got.Root, err)
	}

	// 2 回目は lock 済みコミットでキャッシュが再利用される。
	req.Commit = got.Commit
	again, err := g.Fetch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if again.Root != got.Root {
		t.Fatalf("cache was not reused: %q vs %q", again.Root, got.Root)
	}
}

// TestGitFetchTag はタグ参照が解決できることを確かめる。
func TestGitFetchUnknownRef(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network test")
	}
	g := NewGit(store.New(t.TempDir()))
	_, err := g.Fetch(context.Background(), Request{
		Git: "https://github.com/anthropics/skills",
		Ref: "no-such-ref-xyz",
	})
	if err == nil {
		t.Fatal("expected an error for an unknown ref")
	}
}
