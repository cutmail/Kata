package state

import (
	"path/filepath"
	"testing"
)

func TestOriginScoping(t *testing.T) {
	s := New()
	s.Put(Entry{Name: "a", Origin: "/repo1", Dest: "/d1"})
	s.Put(Entry{Name: "a", Origin: "/repo2", Dest: "/d2"})

	// 同名でも由来が違えば別のエントリとして扱う。
	if len(s.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(s.Entries))
	}
	if got := s.ByOrigin("/repo1"); len(got) != 1 || got[0].Dest != "/d1" {
		t.Fatalf("ByOrigin = %+v", got)
	}
	s.Delete("/repo1", "a")
	if _, ok := s.Get("/repo1", "a"); ok {
		t.Fatal("entry should be gone")
	}
	if _, ok := s.Get("/repo2", "a"); !ok {
		t.Fatal("the other origin must be untouched")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	s := New()
	s.Put(Entry{Name: "a", Origin: "/repo", Dest: "/d", Target: "/t", Type: "skill"})
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := got.Get("/repo", "a")
	if !ok || e.Target != "/t" {
		t.Fatalf("entry = %+v, ok = %v", e, ok)
	}
}

func TestLoadMissingFile(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 0 {
		t.Fatal("expected an empty state")
	}
}
