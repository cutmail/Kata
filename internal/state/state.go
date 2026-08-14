// Package state は「kata が実際に何をどこへ配置したか」を記録する。
// kata が作っていないものを消さない、という不変条件はこの記録に依存する。
package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

const CurrentVersion = 1

// State は配置実績の全体。
type State struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Entry は配置実績 1 件。
type Entry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Dest は配置先の絶対パス。
	Dest string `json:"dest"`
	// Target は symlink の指す先の絶対パス。
	Target   string `json:"target"`
	Strategy string `json:"strategy"`
	// Scope は配置スコープ。診断のために記録する。
	Scope string `json:"scope,omitempty"`
	// Digest は copy 戦略で配置した内容のダイジェスト。
	//
	// 実体には「誰が置いたか」が書かれていないため、この記録だけが撤去や
	// 上書きを正当化する根拠になる。空は「証明できない」を意味し、
	// 判定は必ず「触らない」側に倒れる。古い state.json をそのまま読める。
	Digest string `json:"digest,omitempty"`
	// Origin はこの配置を生んだ kata.yml のディレクトリ。
	// 複数リポジトリを併用しても互いの配置を壊さないために持つ。
	Origin string `json:"origin"`
}

// New は空の状態を返す。
func New() *State {
	return &State{Version: CurrentVersion, Entries: []Entry{}}
}

// Load は状態を読み込む。ファイルが無ければ空の状態を返す。
func Load(path string) (*State, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	if s.Entries == nil {
		s.Entries = []Entry{}
	}
	return &s, nil
}

// Save は状態を書き出す。
func (s *State) Save(path string) error {
	s.Version = CurrentVersion
	sort.Slice(s.Entries, func(i, j int) bool {
		if s.Entries[i].Origin != s.Entries[j].Origin {
			return s.Entries[i].Origin < s.Entries[j].Origin
		}
		return s.Entries[i].Name < s.Entries[j].Name
	})
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ByOrigin は指定した kata.yml 由来のエントリだけを返す。
func (s *State) ByOrigin(origin string) []Entry {
	var out []Entry
	for _, e := range s.Entries {
		if e.Origin == origin {
			out = append(out, e)
		}
	}
	return out
}

// Get は origin と name でエントリを引く。
func (s *State) Get(origin, name string) (Entry, bool) {
	for _, e := range s.Entries {
		if e.Origin == origin && e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Put はエントリを登録する。同じ origin と name があれば置き換える。
func (s *State) Put(e Entry) {
	for i := range s.Entries {
		if s.Entries[i].Origin == e.Origin && s.Entries[i].Name == e.Name {
			s.Entries[i] = e
			return
		}
	}
	s.Entries = append(s.Entries, e)
}

// Delete は origin と name でエントリを取り除く。
func (s *State) Delete(origin, name string) {
	for i := range s.Entries {
		if s.Entries[i].Origin == origin && s.Entries[i].Name == name {
			s.Entries = append(s.Entries[:i], s.Entries[i+1:]...)
			return
		}
	}
}
