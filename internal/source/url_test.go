package source

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cutmail/kata/internal/digest"
	"github.com/cutmail/kata/internal/store"
)

// mkTarGz は skills/pdf/SKILL.md を 1 件だけ含む tar.gz を組み立てる。
func mkTarGz(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	write := func(name string, mode int64, content string, dir bool) {
		t.Helper()
		h := &tar.Header{Name: name, Mode: mode, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if dir {
			h.Typeflag, h.Size = tar.TypeDir, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if !dir {
			if _, err := tw.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("skills/", 0o755, "", true)
	write("skills/pdf/", 0o755, "", true)
	write("skills/pdf/SKILL.md", 0o644, body, false)

	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// serveArchive は書庫を返すサーバを立て、URL と取得回数を返す。
func serveArchive(t *testing.T, payload []byte) (string, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/skills.tar.gz", &hits
}

func TestURLFetchExtractsArchive(t *testing.T) {
	payload := mkTarGz(t, "hello\n")
	url, _ := serveArchive(t, payload)
	u := NewURL(store.New(t.TempDir()))

	got, err := u.Fetch(context.Background(), Request{URL: url, Path: "skills/pdf"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(got.Root, "SKILL.md"))
	if err != nil || string(body) != "hello\n" {
		t.Fatalf("extracted content = %q, %v", body, err)
	}
	// 検証に使えるよう、取得物のダイジェストを返すこと。
	want, err := digest.Sum(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != want {
		t.Fatalf("digest = %s, want %s", got.Digest, want)
	}
}

// TestURLFetchVerifiesDigest は、期待と違うバイト列を展開しないことを確かめる。
// 検証してから展開する順序が、この取得元の安全性そのものになっている。
func TestURLFetchVerifiesDigest(t *testing.T) {
	url, _ := serveArchive(t, mkTarGz(t, "hello\n"))
	s := store.New(t.TempDir())
	u := NewURL(s)

	const wrong = digest.Prefix + "0000000000000000000000000000000000000000000000000000000000000000"
	_, err := u.Fetch(context.Background(), Request{URL: url, Path: "skills/pdf", Digest: wrong})
	if err == nil {
		t.Fatal("expected a checksum mismatch to fail")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
	// 展開まで進んでいないこと。
	if s.Has(store.ArchiveKey(url, wrong)) {
		t.Fatal("the archive must not be extracted before its digest is verified")
	}
	entries, err := os.ReadDir(s.ReposRoot())
	if err == nil && len(entries) > 0 {
		t.Fatalf("nothing should have been promoted into the cache, found %d entries", len(entries))
	}
}

// TestURLFetchUsesCacheWhenPinned は、固定済みならネットワークに出ないことを
// 確かめる。これがあるおかげで、lock 済みのマニフェストはオフラインで sync できる。
func TestURLFetchUsesCacheWhenPinned(t *testing.T) {
	payload := mkTarGz(t, "hello\n")
	url, hits := serveArchive(t, payload)
	u := NewURL(store.New(t.TempDir()))

	first, err := u.Fetch(context.Background(), Request{URL: url, Path: "skills/pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("requests = %d, want 1", hits.Load())
	}

	again, err := u.Fetch(context.Background(), Request{URL: url, Path: "skills/pdf", Digest: first.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("requests = %d after a pinned fetch, want it served from the cache", hits.Load())
	}
	if again.Root != first.Root {
		t.Fatalf("cached root = %q, want %q", again.Root, first.Root)
	}
}

// TestURLFetchRejectsPlainHTTPToNonLoopback は、平文での取得を拒むことを確かめる。
func TestURLFetchRejectsPlainHTTPToNonLoopback(t *testing.T) {
	u := NewURL(store.New(t.TempDir()))
	_, err := u.Fetch(context.Background(), Request{URL: "http://example.com/skills.tar.gz"})
	if err == nil || !strings.Contains(err.Error(), "https is required") {
		t.Fatalf("err = %v, want it to require https", err)
	}
}

// TestURLFetchRejectsBadStatus は、200 以外を失敗として扱うことを確かめる。
func TestURLFetchRejectsBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	u := NewURL(store.New(t.TempDir()))
	_, err := u.Fetch(context.Background(), Request{URL: srv.URL + "/x.tar.gz"})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want the status to be reported", err)
	}
}

// TestURLFetchRejectsUnknownFormat は、書庫でないものを拒むことを確かめる。
func TestURLFetchRejectsUnknownFormat(t *testing.T) {
	url, _ := serveArchive(t, []byte("just some text, not an archive"))
	u := NewURL(store.New(t.TempDir()))

	if _, err := u.Fetch(context.Background(), Request{URL: url}); err == nil {
		t.Fatal("expected a non-archive payload to be rejected")
	}
}

// TestURLFetchRejectsMissingSubpath は、書庫内に無いサブパスを拒むことを確かめる。
func TestURLFetchRejectsMissingSubpath(t *testing.T) {
	url, _ := serveArchive(t, mkTarGz(t, "hello\n"))
	u := NewURL(store.New(t.TempDir()))

	if _, err := u.Fetch(context.Background(), Request{URL: url, Path: "skills/nope"}); err == nil {
		t.Fatal("expected a missing subpath to be rejected")
	}
}
