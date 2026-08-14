package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepoKeyIsStableAndCommitScoped(t *testing.T) {
	a := RepoKey("https://example.com/repo", "abcdef1234567890")
	b := RepoKey("https://example.com/repo", "abcdef1234567890")
	if a != b {
		t.Fatalf("key is not stable: %q vs %q", a, b)
	}
	if c := RepoKey("https://example.com/repo", "0000000000000000"); c == a {
		t.Fatal("different commits must produce different keys")
	}
	if d := RepoKey("https://example.com/other", "abcdef1234567890"); d == a {
		t.Fatal("different URLs must produce different keys")
	}
}

func TestPromoteMovesStagingIntoPlace(t *testing.T) {
	s := New(t.TempDir())
	staging, err := s.NewStaging()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(staging, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	key := RepoKey("https://example.com/repo", "deadbeefcafe")
	if s.Has(key) {
		t.Fatal("store should be empty")
	}
	dir, err := s.Promote(repo, key)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Has(key) {
		t.Fatal("promoted entry should exist")
	}
	body, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	if err != nil || string(body) != "hi" {
		t.Fatalf("content = %q, %v", body, err)
	}
	s.Discard(staging)
}

func TestPromoteKeepsExistingEntry(t *testing.T) {
	s := New(t.TempDir())
	key := RepoKey("https://example.com/repo", "deadbeefcafe")

	// 先に置かれた内容が勝つ。
	first := mkStaging(t, s, "first")
	if _, err := s.Promote(first, key); err != nil {
		t.Fatal(err)
	}
	second := mkStaging(t, s, "second")
	dir, err := s.Promote(second, key)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "f.txt"))
	if err != nil || string(body) != "first" {
		t.Fatalf("content = %q, %v; the existing entry must win", body, err)
	}
}

func mkStaging(t *testing.T, s *Store, body string) string {
	t.Helper()
	staging, err := s.NewStaging()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(staging, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo
}
