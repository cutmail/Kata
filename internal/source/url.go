package source

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/cutmail/kata/internal/digest"
	"github.com/cutmail/kata/internal/store"
)

const (
	// maxDownloadBytes は取得する書庫本体の上限。展開後の上限とは別に、
	// 読み込み段階でも歯止めをかける。
	maxDownloadBytes = 256 << 20
	// downloadTimeout は 1 件の取得にかける時間の上限。
	downloadTimeout = 5 * time.Minute
	// maxRedirects は追うリダイレクトの数の上限。
	maxRedirects = 10
)

// URL は書庫（tar.gz / tgz / zip）を HTTP で取得して展開する。
type URL struct {
	Store  *store.Store
	Client *http.Client
}

// NewURL は URL フェッチャを返す。
func NewURL(s *store.Store) *URL {
	return &URL{Store: s, Client: newDownloadClient()}
}

// newDownloadClient は取得用の HTTP クライアントを組み立てる。
func newDownloadClient() *http.Client {
	return &http.Client{
		Timeout: downloadTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			// リダイレクト先には loopback の例外を認めない。
			// 例外は「手元で試すサーバをマニフェストに書く」ためのものであり、
			// 取得元がリダイレクトで利用者のローカルサービスへ GET を
			// 撃ち込む口にしてはならない。
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing to follow a redirect to %q; https is required", req.URL.Scheme)
			}
			return nil
		},
	}
}

// Fetch は書庫を取得し、検証してから展開してサブパスを解決する。
//
// 手順の順序そのものが安全性になっている。ダイジェストを照合してから展開するので、
// 期待と違うバイト列が展開されることはない。
func (u *URL) Fetch(ctx context.Context, req Request) (Fetched, error) {
	// lock 済みで既にキャッシュがあれば、ネットワークに一切出ない。
	// これがあるおかげで、固定済みのマニフェストはオフラインでも sync できる。
	if req.Digest != "" {
		key := store.ArchiveKey(req.URL, req.Digest)
		if u.Store.Has(key) {
			root, err := resolveSubpath(u.Store.Dir(key), req.Path)
			if err != nil {
				return Fetched{}, err
			}
			return Fetched{Root: root, Digest: req.Digest}, nil
		}
	}

	staging, err := u.Store.NewStaging()
	if err != nil {
		return Fetched{}, err
	}
	defer u.Store.Discard(staging)

	archive := filepath.Join(staging, "archive")
	got, err := u.download(ctx, req.URL, archive)
	if err != nil {
		return Fetched{}, fmt.Errorf("fetch %s: %w", req.URL, err)
	}
	// 期待値があるなら、展開する前に必ず照合する。
	if req.Digest != "" && !digest.Equal(got, req.Digest) {
		return Fetched{}, fmt.Errorf("checksum mismatch for %s: got %s, want %s",
			req.URL, got, req.Digest)
	}

	tree := filepath.Join(staging, "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		return Fetched{}, err
	}
	if err := extract(archive, tree); err != nil {
		return Fetched{}, fmt.Errorf("extract %s: %w", req.URL, err)
	}

	dir, err := u.Store.Promote(tree, store.ArchiveKey(req.URL, got))
	if err != nil {
		return Fetched{}, err
	}
	root, err := resolveSubpath(dir, req.Path)
	if err != nil {
		return Fetched{}, err
	}
	return Fetched{Root: root, Digest: got}, nil
}

// download は書庫を dst へ書き出し、その内容ダイジェストを返す。
//
// 本体をメモリに載せず、書きながら同時にハッシュを計算する。
func (u *URL) download(ctx context.Context, raw, dst string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if err := checkScheme(parsed); err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	client := u.Client
	if client == nil {
		client = newDownloadClient()
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s", resp.Status)
	}

	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	// Content-Length は自己申告なので信用せず、実際に読んだ量で打ち切る。
	// 上限ちょうどで止まったのか、その先があったのかを見分けるため 1 バイト多く読む。
	limited := io.LimitReader(resp.Body, maxDownloadBytes+1)
	n, err := io.Copy(f, limited)
	if err != nil {
		return "", err
	}
	if n > maxDownloadBytes {
		return "", fmt.Errorf("refusing to download more than %d bytes", int64(maxDownloadBytes))
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	// 書き終えたものをそのまま読み直してハッシュする。
	// 書き込み中に計算した値ではなく、実際にディスクにある内容を根拠にする。
	r, err := os.Open(dst)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	return digest.Sum(r)
}

// checkScheme は取得先の方式が許されるかを確かめる。
//
// 平文で取ってきたものをそのまま配置すると、経路上で差し替えられても
// 気づけない。試験用にサーバを手元で立てられるよう、loopback だけは許す。
func checkScheme(u *url.URL) error {
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && isLoopback(u.Hostname()):
		return nil
	}
	return fmt.Errorf("refusing to fetch %s over %q; https is required", u.Host, u.Scheme)
}

// isLoopback は手元を指すホスト名かを返す。
func isLoopback(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}
