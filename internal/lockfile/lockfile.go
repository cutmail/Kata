// Package lockfile は kata.lock の読み書きを担う。
// lock は「解決済みの取得元」を固定し、別マシンでの再現性を保証する。
package lockfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

const CurrentVersion = 1

// FileName はロックファイルの既定ファイル名。
const FileName = "kata.lock"

// Lock はロックファイル全体。
type Lock struct {
	Version     int     `yaml:"version"`
	GeneratedAt string  `yaml:"generated_at,omitempty"`
	Resolved    []Entry `yaml:"resolved"`
}

// Entry は解決済みパッケージ 1 件。
type Entry struct {
	Name    string `yaml:"name"`
	Type    string `yaml:"type"`
	Source  string `yaml:"source"`
	Ref     string `yaml:"ref,omitempty"`
	Commit  string `yaml:"commit,omitempty"`
	Subpath string `yaml:"subpath,omitempty"`
	Local   string `yaml:"local,omitempty"`
	Digest  string `yaml:"digest,omitempty"`
}

// New は空のロックを返す。
func New() *Lock {
	return &Lock{Version: CurrentVersion, Resolved: []Entry{}}
}

// Load はロックを読み込む。ファイルが無ければ空のロックを返す。
func Load(path string) (*Lock, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	var l Lock
	if err := yaml.Unmarshal(raw, &l); err != nil {
		return nil, err
	}
	if l.Resolved == nil {
		l.Resolved = []Entry{}
	}
	if err := l.validate(); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &l, nil
}

var (
	commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// validate は記録された値が期待する書式に収まっていることを確かめる。
//
// lock は clone してきたリポジトリに含まれる。commit にブランチ名やリビジョン式を
// 書かれると浮動参照として解決されてしまい、固定という前提が崩れる。
// digest も同様に、キャッシュキーの材料になる以上は素性を確かめてから使う。
func (l *Lock) validate() error {
	if l.Version != CurrentVersion {
		return fmt.Errorf("unsupported lock version %d (expected %d)", l.Version, CurrentVersion)
	}
	for _, e := range l.Resolved {
		if e.Commit != "" && !commitRe.MatchString(e.Commit) {
			return fmt.Errorf("entry %q: commit must be 40 lowercase hex characters", e.Name)
		}
		if e.Digest != "" && !digestRe.MatchString(e.Digest) {
			return fmt.Errorf("entry %q: digest must look like sha256:<64 hex>", e.Name)
		}
	}
	return nil
}

// Save はロックを書き出す。差分を安定させるため名前順に整列する。
func (l *Lock) Save(path string, now time.Time) error {
	l.Version = CurrentVersion
	l.GeneratedAt = now.UTC().Format(time.RFC3339)
	sort.Slice(l.Resolved, func(i, j int) bool { return l.Resolved[i].Name < l.Resolved[j].Name })
	out, err := yaml.Marshal(l)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Get は名前でエントリを引く。
func (l *Lock) Get(name string) (Entry, bool) {
	for _, e := range l.Resolved {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Put はエントリを登録する。同名があれば置き換える。
func (l *Lock) Put(e Entry) {
	for i := range l.Resolved {
		if l.Resolved[i].Name == e.Name {
			l.Resolved[i] = e
			return
		}
	}
	l.Resolved = append(l.Resolved, e)
}

// Delete は名前でエントリを取り除く。
func (l *Lock) Delete(name string) {
	for i := range l.Resolved {
		if l.Resolved[i].Name == name {
			l.Resolved = append(l.Resolved[:i], l.Resolved[i+1:]...)
			return
		}
	}
}
