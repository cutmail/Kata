// Package store は取得した実体のキャッシュ領域を管理する。
// 内容は常に再取得可能であり、丸ごと削除しても sync で復元できる。
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store はキャッシュ領域のルート。
type Store struct {
	Root string
}

// New はキャッシュ領域を表す Store を返す。
func New(root string) *Store { return &Store{Root: root} }

// RepoKey は取得元 URL と commit からキャッシュキーを組み立てる。
// commit を含めるため、参照先が変われば別のディレクトリになる。
func RepoKey(url, commit string) string {
	return key(url, commit)
}

// ArchiveKey は取得先 URL と内容ダイジェストからキャッシュキーを組み立てる。
//
// 書庫には commit に相当するものが無く、同じ URL が明日には別のバイト列を
// 返しうる。内容ダイジェストをキーに含めることで、中身が変われば必ず
// 別のディレクトリになる。
func ArchiveKey(url, digest string) string {
	// "sha256:" のような接頭辞はキーの見た目を乱すだけなので落とす。
	if _, rest, ok := strings.Cut(digest, ":"); ok {
		digest = rest
	}
	return key(url, digest)
}

// key は URL と識別子からキャッシュキーを組み立てる。
//
// どちらも切り詰めない。キャッシュがヒットすると取得も検証も行わずにその内容を
// 返すため、キーの衝突がそのまま「検証をすり抜けた配置」になる。
// 見た目の短さより、衝突を総当たりで作れないことを優先する。
func key(url, ident string) string {
	sum := sha256.Sum256([]byte(url))
	identSum := sha256.Sum256([]byte(ident))
	return fmt.Sprintf("%s-%s", hex.EncodeToString(sum[:]), hex.EncodeToString(identSum[:]))
}

// ReposRoot は取得物キャッシュのルートを返す。
func (s *Store) ReposRoot() string { return filepath.Join(s.Root, "repos") }

// StagingRoot は取得作業用の一時領域のルートを返す。
func (s *Store) StagingRoot() string { return filepath.Join(s.Root, "staging") }

// Dir はキーに対応するディレクトリの絶対パスを返す。
func (s *Store) Dir(key string) string { return filepath.Join(s.ReposRoot(), key) }

// Has はキーに対応する実体が既に存在するかを返す。
func (s *Store) Has(key string) bool {
	fi, err := os.Stat(s.Dir(key))
	return err == nil && fi.IsDir()
}

// NewStaging は取得作業用の一時ディレクトリを作って返す。
// 取得が完全に終わるまで本来の場所には置かない。
func (s *Store) NewStaging() (string, error) {
	base := s.StagingRoot()
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	return os.MkdirTemp(base, "fetch-")
}

// Promote は取得が完了した一時ディレクトリを正規のキャッシュ位置へ移す。
// 既に同じキーが存在する場合は一時ディレクトリを破棄する（先勝ち）。
func (s *Store) Promote(staging, key string) (string, error) {
	dst := s.Dir(key)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if s.Has(key) {
		_ = os.RemoveAll(staging)
		return dst, nil
	}
	if err := os.Rename(staging, dst); err != nil {
		// 別プロセスが先に置いた場合は勝者を採用する。
		if s.Has(key) {
			_ = os.RemoveAll(staging)
			return dst, nil
		}
		return "", err
	}
	return dst, nil
}

// Discard は一時ディレクトリを破棄する。
func (s *Store) Discard(staging string) {
	if staging != "" {
		_ = os.RemoveAll(staging)
	}
}
