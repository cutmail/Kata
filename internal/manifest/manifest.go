// Package manifest は kata.yml の読み書き・正規化・検証を担う。
package manifest

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cutmail/kata/internal/redact"
)

// CurrentVersion はこの実装が解釈できるマニフェストのバージョン。
const CurrentVersion = 1

// FileName はマニフェストの既定ファイル名。
const FileName = "kata.yml"

// パッケージ種別。
const (
	TypeSkill   = "skill"
	TypeCommand = "command"
	// TypeAgent は Claude Code のサブエージェント定義（単一の .md）。
	TypeAgent = "agent"
)

// 配置スコープ。
const (
	// ScopeUser は利用者の設定ディレクトリ（通常は ~/.claude）へ配置する。
	ScopeUser = "user"
	// ScopeProject は kata.yml と同じ階層の .claude へ配置する。
	ScopeProject = "project"
)

// 配置戦略。
const (
	// StrategyLink は配置先から取得物へ symlink を張る。
	StrategyLink = "link"
	// StrategyCopy は実体を複製する。symlink が使えない環境や、
	// 配置先ごとリポジトリで共有したい場合に使う。
	StrategyCopy = "copy"
	// StrategyAuto は symlink が使えれば link、使えなければ copy を選ぶ。
	StrategyAuto = "auto"
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ValidName はパッケージ名として使える文字列かを返す。
// import が取り込む前に候補を弾けるよう、検証と同じ規則を外へ出している。
func ValidName(name string) bool { return nameRe.MatchString(name) }

// Manifest は kata.yml 全体を表す。
type Manifest struct {
	Version  int               `yaml:"version"`
	Defaults Defaults          `yaml:"defaults,omitempty"`
	Sources  map[string]Source `yaml:"sources,omitempty"`
	Packages []Package         `yaml:"packages"`

	// dir はマニフェストが置かれたディレクトリ。local: の基準パスになる。
	dir string
}

// Defaults はパッケージ個別指定がない場合の既定値。
type Defaults struct {
	Scope    string `yaml:"scope,omitempty"`
	Strategy string `yaml:"strategy,omitempty"`
}

// Source は sources: に定義する再利用可能な取得元。
type Source struct {
	Git string `yaml:"git,omitempty"`
	Ref string `yaml:"ref,omitempty"`
	// URL は書庫の取得先。Git と排他。
	URL string `yaml:"url,omitempty"`
}

// Package は管理対象 1 件。
type Package struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`

	// 取得元。git / url / local のいずれか（from は共有取得元の別名解決）。
	From  string `yaml:"from,omitempty"`
	Git   string `yaml:"git,omitempty"`
	Ref   string `yaml:"ref,omitempty"`
	Path  string `yaml:"path,omitempty"`
	Local string `yaml:"local,omitempty"`
	// URL は書庫（tar.gz / tgz / zip）の取得先。Git・Local と排他。
	URL string `yaml:"url,omitempty"`
	// SHA256 は書庫本体に期待する 16 進 64 文字のダイジェスト。任意。
	SHA256 string `yaml:"sha256,omitempty"`

	Scope    string `yaml:"scope,omitempty"`
	Strategy string `yaml:"strategy,omitempty"`

	// Profiles は所属プロファイル。空なら常に選択される。
	Profiles []string `yaml:"profiles,omitempty"`
}

// HasProfile は p が profile に属するかを返す。
//
// profiles を宣言していないパッケージは、どの profile を指定しても選ばれる。
// 「未指定＝常に選択」を保つことで、profiles を使わない利用者の挙動が変わらない。
func (p Package) HasProfile(profile string) bool {
	if profile == "" || len(p.Profiles) == 0 {
		return true
	}
	return slices.Contains(p.Profiles, profile)
}

// IsLocal はリポジトリ同梱の実体を指すかを返す。
func (p Package) IsLocal() bool { return p.Local != "" }

// IsURL は書庫を取得先とするかを返す。
func (p Package) IsURL() bool { return p.URL != "" }

// SourceID は lock に記録する取得元の識別子。
//
// 資格情報は落とす。kata.lock はコミットされる前提のファイルであり、
// URL に埋め込まれたトークンをそのまま書くと公開リポジトリへ流出する。
// 同一性の判定に資格情報は要らない。
func (p Package) SourceID() string {
	switch {
	case p.IsLocal():
		return "local:" + filepath.ToSlash(p.Local)
	case p.IsURL():
		return "url+" + redact.URL(p.URL)
	}
	return "git+" + redact.URL(p.Git)
}

// Dir はマニフェストが置かれたディレクトリを返す。
func (m *Manifest) Dir() string { return m.dir }

// Profiles は宣言されたプロファイル名の和集合を昇順で返す。
// 指定された profile が実在するかを確かめるために使う。
func (m *Manifest) Profiles() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range m.Packages {
		for _, prof := range p.Profiles {
			if !seen[prof] {
				seen[prof] = true
				out = append(out, prof)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Find は名前でパッケージを引く。
func (m *Manifest) Find(name string) (Package, bool) {
	for _, p := range m.Packages {
		if p.Name == name {
			return p, true
		}
	}
	return Package{}, false
}

// Remove は名前でパッケージを取り除く。取り除けたかを返す。
func (m *Manifest) Remove(name string) bool {
	for i, p := range m.Packages {
		if p.Name == name {
			m.Packages = append(m.Packages[:i], m.Packages[i+1:]...)
			return true
		}
	}
	return false
}

// Add はパッケージを追加する。同名が既にあればエラー。
func (m *Manifest) Add(p Package) error {
	if _, ok := m.Find(p.Name); ok {
		return fmt.Errorf("package %q already exists", p.Name)
	}
	m.Packages = append(m.Packages, p)
	return nil
}

// New は空のマニフェストを返す。
func New(dir string) *Manifest {
	return &Manifest{
		Version:  CurrentVersion,
		Defaults: Defaults{Scope: ScopeUser, Strategy: StrategyLink},
		Packages: []Package{},
		dir:      dir,
	}
}

// Load はマニフェストを読み込み、正規化と検証まで済ませて返す。
func Load(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	m.dir = filepath.Dir(abs)

	if err := m.normalize(); err != nil {
		return nil, err
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Save はマニフェストを書き出す。書き込みは一時ファイル経由で原子的に行う。
func (m *Manifest) Save(path string) error {
	out, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// normalize は from の解決と既定値の適用を行う。
func (m *Manifest) normalize() error {
	if m.Defaults.Scope == "" {
		m.Defaults.Scope = ScopeUser
	}
	if m.Defaults.Strategy == "" {
		m.Defaults.Strategy = StrategyLink
	}
	for i := range m.Packages {
		p := &m.Packages[i]
		if p.From != "" {
			src, ok := m.Sources[p.From]
			if !ok {
				return fmt.Errorf("package %q: unknown source %q", p.Name, p.From)
			}
			if p.Git == "" {
				p.Git = src.Git
			}
			if p.Ref == "" {
				p.Ref = src.Ref
			}
			if p.URL == "" {
				p.URL = src.URL
			}
		}
		if p.Scope == "" {
			p.Scope = m.Defaults.Scope
		}
		if p.Strategy == "" {
			p.Strategy = m.Defaults.Strategy
		}
		p.Profiles = normalizeProfiles(p.Profiles)
	}
	return nil
}

// normalizeProfiles は前後の空白を落とし、並べ替えて重複を除く。
// 書き出したときの差分を安定させ、比較を単純にするため。
func normalizeProfiles(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// Validate はマニフェスト全体の妥当性を検証する。
func (m *Manifest) Validate() error {
	if m.Version != CurrentVersion {
		return fmt.Errorf("unsupported manifest version %d (expected %d)", m.Version, CurrentVersion)
	}
	seen := map[string]bool{}
	for _, p := range m.Packages {
		if err := validatePackage(p); err != nil {
			return err
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate package name %q", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

func validatePackage(p Package) error {
	if p.Name == "" {
		return fmt.Errorf("package name is required")
	}
	if !nameRe.MatchString(p.Name) {
		return fmt.Errorf("package %q: name must match %s", p.Name, nameRe)
	}
	switch p.Type {
	case TypeSkill, TypeCommand, TypeAgent:
	case "":
		return fmt.Errorf("package %q: type is required", p.Name)
	default:
		return fmt.Errorf("package %q: unsupported type %q", p.Name, p.Type)
	}
	switch p.Scope {
	case ScopeUser, ScopeProject:
	default:
		return fmt.Errorf("package %q: unsupported scope %q", p.Name, p.Scope)
	}
	for _, prof := range p.Profiles {
		// 表記ゆれで --profile と一致せず、黙って選択から外れる事故を防ぐ。
		if !nameRe.MatchString(prof) {
			return fmt.Errorf("package %q: profile %q must match %s", p.Name, prof, nameRe)
		}
	}
	switch p.Strategy {
	case StrategyLink, StrategyCopy, StrategyAuto:
	default:
		return fmt.Errorf("package %q: unsupported strategy %q", p.Name, p.Strategy)
	}

	hasGit, hasLocal, hasURL := p.Git != "", p.Local != "", p.URL != ""
	switch n := boolCount(hasGit, hasLocal, hasURL); {
	case n > 1:
		return fmt.Errorf("package %q: git, url and local are mutually exclusive", p.Name)
	case n == 0:
		return fmt.Errorf("package %q: one of git, url, from or local is required", p.Name)
	}
	if hasURL {
		if err := validateURL(p); err != nil {
			return err
		}
	}
	if hasGit {
		if err := validateGit(p); err != nil {
			return err
		}
	}
	if p.SHA256 != "" && !hasURL {
		return fmt.Errorf("package %q: sha256 only applies to a url source", p.Name)
	}
	if hasLocal {
		if p.Path != "" {
			return fmt.Errorf("package %q: path cannot be combined with local", p.Name)
		}
		if err := checkRelPath(p.Name, "local", p.Local); err != nil {
			return err
		}
	}
	if p.Path != "" {
		if err := checkRelPath(p.Name, "path", p.Path); err != nil {
			return err
		}
	}
	return nil
}

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// validateURL は url 取得元の指定を検証する。
func validateURL(p Package) error {
	u, err := url.Parse(p.URL)
	if err != nil {
		return fmt.Errorf("package %q: url is not a valid URL: %w", p.Name, err)
	}
	// 平文で取ってきたものを配置すると、経路上で差し替えられても気づけない。
	// 手元で立てたサーバだけは、試すために許す。
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return fmt.Errorf("package %q: url must use https (http is allowed for loopback only)", p.Name)
	}
	if p.Ref != "" {
		return fmt.Errorf("package %q: ref is meaningless for a url source", p.Name)
	}
	if p.SHA256 != "" && !sha256Re.MatchString(p.SHA256) {
		return fmt.Errorf("package %q: sha256 must be 64 lowercase hex characters", p.Name)
	}
	return nil
}

// validateGit は git 取得元の指定を検証する。
//
// url 取得元だけ https を求めて git は素通し、という非対称をなくす。
// git:// と http:// は経路上で中身を差し替えられ、初回解決がそのまま lock に
// 固定されるため利用者は気づけない。
//
// file:// とローカルパスは、リポジトリ内にミラーを置く運用やエアギャップ環境で
// 正当に使われるので受け入れる。ただし攻撃者由来のマニフェストが利用者の
// 私有リポジトリを ~/.claude へ引き込む経路にもなるため、
// マニフェストのディレクトリ配下に収まることを取得時に確かめる（source.Git）。
func validateGit(p Package) error {
	// scp 形式（git@host:path）は URL として解析できないが、ssh なので許す。
	if strings.HasPrefix(p.Git, "git@") || scpLikeRe.MatchString(p.Git) {
		return nil
	}
	// Windows の絶対パス（C:\...）は url.Parse がドライブレターをスキームと
	// 誤認識する（scheme "c" と判定される）ため、URL 解析より先にローカルパス
	// として判定する。
	if filepath.IsAbs(p.Git) {
		return nil
	}
	u, err := url.Parse(p.Git)
	if err != nil {
		return fmt.Errorf("package %q: git is not a valid URL: %w", p.Name, err)
	}
	switch u.Scheme {
	case "https", "ssh", "file", "":
		return nil
	}
	return fmt.Errorf(
		"package %q: git must use https, ssh or a local path (got %q); "+
			"plain git:// and http:// can be tampered with in transit", p.Name, u.Scheme)
}

// IsLocalGit は git 取得元が手元のパスを指すかを返す。
func (p Package) IsLocalGit() bool {
	if p.Git == "" {
		return false
	}
	if strings.HasPrefix(p.Git, "file://") {
		return true
	}
	return !strings.Contains(p.Git, "://") && !strings.HasPrefix(p.Git, "git@") &&
		!scpLikeRe.MatchString(p.Git)
}

// LocalGitPath は手元を指す git 取得元のパスを返す。
func (p Package) LocalGitPath() string {
	return strings.TrimPrefix(p.Git, "file://")
}

// scpLikeRe は host:path 形式の ssh 指定にあたる。
var scpLikeRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9._-]+:`)

// isLoopbackHost は手元を指すホスト名かを返す。
func isLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// boolCount は真の数を数える。
func boolCount(bs ...bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

// checkRelPath は相対パスであることと、基準ディレクトリの外へ抜けないことを確認する。
func checkRelPath(pkg, field, v string) error {
	// kata.yml は複数 OS で共有される前提なので、実行環境の filepath.IsAbs だけでは
	// 足りない。Windows 上で filepath.IsAbs("/etc/passwd") は false になる
	// （ボリューム名を欠くため）ので、Unix 形式の絶対パスも別途弾く。
	if filepath.IsAbs(v) || path.IsAbs(filepath.ToSlash(v)) {
		return fmt.Errorf("package %q: %s must be a relative path", pkg, field)
	}
	clean := filepath.Clean(v)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("package %q: %s must not escape its base directory", pkg, field)
	}
	return nil
}
