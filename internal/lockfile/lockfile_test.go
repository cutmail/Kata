package lockfile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingFileReturnsEmptyLock(t *testing.T) {
	l, err := Load(filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Resolved) != 0 {
		t.Fatalf("expected an empty lock, got %+v", l.Resolved)
	}
}

func TestPutGetDelete(t *testing.T) {
	l := New()
	l.Put(Entry{Name: "a", Commit: "1"})
	l.Put(Entry{Name: "a", Commit: "2"})
	if len(l.Resolved) != 1 {
		t.Fatalf("put should replace by name, got %d entries", len(l.Resolved))
	}
	e, ok := l.Get("a")
	if !ok || e.Commit != "2" {
		t.Fatalf("entry = %+v, ok = %v", e, ok)
	}
	l.Delete("a")
	if _, ok := l.Get("a"); ok {
		t.Fatal("entry should be gone")
	}
}

func TestSaveSortsEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	l := New()
	l.Put(Entry{Name: "z"})
	l.Put(Entry{Name: "a"})
	if err := l.Save(path, time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Resolved[0].Name != "a" || got.Resolved[1].Name != "z" {
		t.Fatalf("entries are not sorted: %+v", got.Resolved)
	}
	if got.GeneratedAt == "" {
		t.Fatal("generated_at should be recorded")
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temporary file should not be left behind")
	}
}
