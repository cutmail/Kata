package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cutmail/kata/internal/linker"
	"github.com/cutmail/kata/internal/lockfile"
	"github.com/cutmail/kata/internal/manifest"
)

// fixture はネットワークに依存しない検証環境を組み立てる。
type fixture struct {
	cfg    Config
	repo   string // kata.yml を置くディレクトリ
	claude string // 配置先
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(repo); err != nil {
		t.Fatal(err)
	}
	return fixture{
		cfg: Config{
			ManifestPath: filepath.Join(repo, manifest.FileName),
			KataHome:     filepath.Join(base, "kata-home"),
			ClaudeHome:   filepath.Join(base, "claude"),
		},
		repo:   repo,
		claude: filepath.Join(base, "claude"),
	}
}

// addSkill は local/ 配下に skill の実体を作る。
func (f fixture) addSkill(t *testing.T, name, body string) string {
	t.Helper()
	dir := filepath.Join(f.repo, "local", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// addCommand は local/ 配下に command の実体を作る。
func (f fixture) addCommand(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(f.repo, "local", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name+".md")
	if err := os.WriteFile(p, []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// addAgent は local/ 配下に agent の実体を作る。
func (f fixture) addAgent(t *testing.T, name, body string) string {
	t.Helper()
	dir := filepath.Join(f.repo, "local", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name+".md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func (f fixture) open(t *testing.T) *App {
	t.Helper()
	a, err := Open(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// declare はマニフェストにパッケージを直接書き込む。
func (f fixture) declare(t *testing.T, pkgs ...manifest.Package) {
	t.Helper()
	a := f.open(t)
	for _, p := range pkgs {
		if p.Scope == "" {
			p.Scope = manifest.ScopeUser
		}
		if p.Strategy == "" {
			p.Strategy = manifest.StrategyLink
		}
		if err := a.man.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.man.Save(f.cfg.ManifestPath); err != nil {
		t.Fatal(err)
	}
}

func TestSyncDeploysSkillAndCommand(t *testing.T) {
	f := newFixture(t)
	skillDir := f.addSkill(t, "my-review", "# my review\n")
	cmdPath := f.addCommand(t, "pr")
	f.declare(t,
		manifest.Package{Name: "my-review", Type: manifest.TypeSkill, Local: "./local/skills/my-review"},
		manifest.Package{Name: "pr", Type: manifest.TypeCommand, Local: "./local/commands/pr.md"},
	)

	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := rep.Counts()[ActionCreate]; got != 2 {
		t.Fatalf("created = %d, want 2", got)
	}

	assertLink(t, filepath.Join(f.claude, "skills", "my-review"), skillDir)
	assertLink(t, filepath.Join(f.claude, "commands", "pr.md"), cmdPath)

	// リンク越しに中身が読めることまで確認する。
	body, err := os.ReadFile(filepath.Join(f.claude, "skills", "my-review", "SKILL.md"))
	if err != nil || string(body) != "# my review\n" {
		t.Fatalf("content through link = %q, %v", body, err)
	}
}

func TestSyncIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"})

	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Counts()[ActionUnchanged]; got != 1 {
		t.Fatalf("unchanged = %d, want 1 (counts: %v)", got, rep.Counts())
	}
}

func TestSyncWritesLockAndState(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"})

	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	lk, err := lockfile.Load(f.cfg.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	e, ok := lk.Get("a")
	if !ok || e.Source != "local:./local/skills/a" {
		t.Fatalf("lock entry = %+v, ok = %v", e, ok)
	}
	if _, err := os.Stat(f.cfg.StatePath()); err != nil {
		t.Fatalf("state file missing: %v", err)
	}
}

func TestSyncRemovesUndeclaredPackages(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.addSkill(t, "b", "y")
	f.declare(t,
		manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"},
		manifest.Package{Name: "b", Type: manifest.TypeSkill, Local: "./local/skills/b"},
	)
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}

	// b を宣言から外すと、配置も撤去されなければならない。
	a := f.open(t)
	a.man.Remove("b")
	if err := a.man.Save(f.cfg.ManifestPath); err != nil {
		t.Fatal(err)
	}
	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Counts()[ActionRemove]; got != 1 {
		t.Fatalf("removed = %d, want 1", got)
	}
	if _, err := os.Lstat(filepath.Join(f.claude, "skills", "b")); !os.IsNotExist(err) {
		t.Fatal("b should have been undeployed")
	}
	assertLink(t, filepath.Join(f.claude, "skills", "a"), filepath.Join(f.repo, "local/skills/a"))

	lk, _ := lockfile.Load(f.cfg.LockPath())
	if _, ok := lk.Get("b"); ok {
		t.Fatal("lock entry for b should be gone")
	}
}

func TestSyncRefusesToOverwriteUnmanagedFile(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"})

	// 利用者が手で置いた既存ディレクトリを模す。
	dest := filepath.Join(f.claude, "skills", "a")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err == nil {
		t.Fatal("expected sync to fail")
	}
	if rep.Failed() != 1 || !errors.Is(rep.Changes[0].Err, linker.ErrOccupied) {
		t.Fatalf("unexpected report: %+v", rep.Changes)
	}
	body, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil || string(body) != "mine" {
		t.Fatalf("existing content was damaged: %q, %v", body, err)
	}

	// --force を付けたときだけ退避して配置する。
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{Force: true}); err != nil {
		t.Fatalf("forced sync: %v", err)
	}
	assertLink(t, dest, filepath.Join(f.repo, "local/skills/a"))
}

func TestSyncDryRunChangesNothing(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"})

	rep, err := f.open(t).Sync(context.Background(), SyncOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Counts()[ActionCreate]; got != 1 {
		t.Fatalf("planned creates = %d, want 1", got)
	}
	if _, err := os.Lstat(filepath.Join(f.claude, "skills", "a")); !os.IsNotExist(err) {
		t.Fatal("dry run must not deploy anything")
	}
	if _, err := os.Stat(f.cfg.LockPath()); !os.IsNotExist(err) {
		t.Fatal("dry run must not write the lock file")
	}
}

func TestSyncRejectsShapeMismatch(t *testing.T) {
	f := newFixture(t)
	f.addCommand(t, "pr")
	// ファイルを skill として宣言する（形が合わない）。
	f.declare(t, manifest.Package{Name: "pr", Type: manifest.TypeSkill, Local: "./local/commands/pr.md"})

	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err == nil {
		t.Fatal("expected sync to fail")
	}
	if !strings.Contains(rep.Changes[0].Err.Error(), "expects a directory") {
		t.Fatalf("unexpected error: %v", rep.Changes[0].Err)
	}
}

func TestSyncWarnsWhenSkillMdIsMissing(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.repo, "local", "skills", "bare")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f.declare(t, manifest.Package{Name: "bare", Type: manifest.TypeSkill, Local: "./local/skills/bare"})

	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Changes[0].Warning == "" {
		t.Fatal("expected a warning about the missing SKILL.md")
	}
}

func TestSyncDeploysAgent(t *testing.T) {
	f := newFixture(t)
	agentPath := f.addAgent(t, "reviewer", "---\nname: reviewer\ndescription: reviews code\n---\n\nbody\n")
	f.declare(t, manifest.Package{Name: "reviewer", Type: manifest.TypeAgent, Local: "./local/agents/reviewer.md"})

	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	assertLink(t, filepath.Join(f.claude, "agents", "reviewer.md"), agentPath)
	if rep.Changes[0].Warning != "" {
		t.Fatalf("unexpected warning: %s", rep.Changes[0].Warning)
	}
}

func TestSyncWarnsWhenAgentHasNoFrontMatter(t *testing.T) {
	f := newFixture(t)
	f.addAgent(t, "bare", "# just a heading\n")
	f.declare(t, manifest.Package{Name: "bare", Type: manifest.TypeAgent, Local: "./local/agents/bare.md"})

	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// front matter が無いと Claude Code に読まれないが、誤配置ではないので警告に留める。
	if rep.Changes[0].Warning == "" {
		t.Fatal("expected a warning about the missing front matter")
	}
	assertLink(t, filepath.Join(f.claude, "agents", "bare.md"), filepath.Join(f.repo, "local/agents/bare.md"))
}

func TestSyncRejectsAgentDeclaredAsDirectory(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	// ディレクトリを agent として宣言する（形が合わない）。
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeAgent, Local: "./local/skills/a"})

	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err == nil {
		t.Fatal("expected sync to fail")
	}
	if !strings.Contains(rep.Changes[0].Err.Error(), "expects a file") {
		t.Fatalf("unexpected error: %v", rep.Changes[0].Err)
	}
}

func TestSyncFailsWhenLocalSourceIsMissing(t *testing.T) {
	f := newFixture(t)
	f.declare(t, manifest.Package{Name: "ghost", Type: manifest.TypeSkill, Local: "./local/skills/ghost"})

	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err == nil {
		t.Fatal("expected sync to fail for a missing source")
	}
}

func TestRemoveUndeploysAndUpdatesManifest(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}

	res, err := f.open(t).Remove(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Unlinked {
		t.Fatal("expected the link to be removed")
	}
	if _, err := os.Lstat(filepath.Join(f.claude, "skills", "a")); !os.IsNotExist(err) {
		t.Fatal("link should be gone")
	}
	m, err := manifest.Load(f.cfg.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Packages) != 0 {
		t.Fatalf("manifest still declares %d packages", len(m.Packages))
	}
	// 実体は消してはならない。
	if _, err := os.Stat(filepath.Join(f.repo, "local/skills/a/SKILL.md")); err != nil {
		t.Fatal("the source must be left untouched")
	}
}

func TestRemoveUnknownPackage(t *testing.T) {
	f := newFixture(t)
	if _, err := f.open(t).Remove(context.Background(), "nope"); err == nil {
		t.Fatal("expected an error for an unknown package")
	}
}

func TestListReportsStatuses(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.addSkill(t, "b", "y")
	f.declare(t,
		manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"},
		manifest.Package{Name: "b", Type: manifest.TypeSkill, Local: "./local/skills/b"},
	)

	// 同期前はどちらも未配置。
	items, err := f.open(t).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Status != StatusMissing {
			t.Fatalf("%s: status = %s, want missing", it.Name, it.Status)
		}
	}

	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}

	// b のリンクを外部から壊してみる。
	dest := filepath.Join(f.claude, "skills", "b")
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.repo, "nowhere"), dest); err != nil {
		t.Fatal(err)
	}

	items, err = f.open(t).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Status{}
	for _, it := range items {
		got[it.Name] = it.Status
	}
	if got["a"] != StatusLinked {
		t.Fatalf("a: status = %s, want linked", got["a"])
	}
	if got["b"] != StatusBroken {
		t.Fatalf("b: status = %s, want broken", got["b"])
	}
}

func TestListReportsOrphans(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}

	a := f.open(t)
	a.man.Remove("a")
	if err := a.man.Save(f.cfg.ManifestPath); err != nil {
		t.Fatal(err)
	}

	items, err := f.open(t).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != StatusOrphan {
		t.Fatalf("items = %+v, want one orphan", items)
	}
}

func TestFindManifestWalksUp(t *testing.T) {
	f := newFixture(t)
	nested := filepath.Join(f.repo, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := findManifest(nested)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(f.cfg.ManifestPath)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != want {
		t.Fatalf("found %q, want %q", gotResolved, want)
	}
}

func TestFindManifestMissing(t *testing.T) {
	if _, err := findManifest(t.TempDir()); !errors.Is(err, ErrManifestNotFound) {
		t.Fatalf("err = %v, want ErrManifestNotFound", err)
	}
}

func TestInitRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(dir); err == nil {
		t.Fatal("expected init to refuse an existing manifest")
	}
}

// addClaudeSkill は配置先に kata 管理外の skill の実体を作る。利用者が手で置いたものを模す。
func (f fixture) addClaudeSkill(t *testing.T, name, body string) string {
	t.Helper()
	dir := filepath.Join(f.claude, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// addClaudeCommand は配置先に kata 管理外の command の実体を作る。
func (f fixture) addClaudeCommand(t *testing.T, name, body string) string {
	t.Helper()
	dir := filepath.Join(f.claude, "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name+".md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// snapshot は root 以下を「相対パス → 内容またはリンク先」で写し取る。
// 「触っていないこと」を 1 行で検証できるようにするために使う。
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out[rel] = "link:" + target
		case d.IsDir():
			out[rel] = "dir"
		default:
			body, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = "file:" + string(body)
		}
		return nil
	})
	// root がまだ無いのは「空」と同じ。
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return out
}

// assertUnchanged は 2 つの写しが完全に一致することを確かめる。
func assertUnchanged(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for k, v := range before {
		if got, ok := after[k]; !ok {
			t.Fatalf("%s: %s disappeared (was %q)", what, k, v)
		} else if got != v {
			t.Fatalf("%s: %s changed from %q to %q", what, k, v, got)
		}
	}
	for k, v := range after {
		if _, ok := before[k]; !ok {
			t.Fatalf("%s: %s appeared (%q)", what, k, v)
		}
	}
}

func assertLink(t *testing.T, dest, want string) {
	t.Helper()
	fi, err := os.Lstat(dest)
	if err != nil {
		t.Fatalf("lstat %s: %v", dest, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink", dest)
	}
	got, err := os.Readlink(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s -> %s, want %s", dest, got, want)
	}
}
