package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cutmail/kata/internal/manifest"
)

// itemsByName は報告を名前で引けるようにする。
func itemsByName(rep *ImportReport) map[string]ImportItem {
	m := map[string]ImportItem{}
	for _, it := range rep.Items {
		m[it.Name] = it
	}
	return m
}

// TestImportLeavesDestinationUntouched は、既定の import が配置先に一切触れないことを
// 確かめる。不変条件「kata が作っていないものは触らない」を import に適用したもの。
func TestImportLeavesDestinationUntouched(t *testing.T) {
	f := newFixture(t)
	f.addClaudeSkill(t, "my-review", "# my review\n")
	f.addClaudeCommand(t, "pr", "# pr\n")

	before := snapshot(t, f.claude)

	rep, err := f.open(t).Import(context.Background(), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Counts()[ImportImported]; got != 2 {
		t.Fatalf("imported = %d, want 2 (items: %+v)", got, rep.Items)
	}

	// 配置先は 1 バイトも変わっていないこと。
	assertUnchanged(t, "deployment target", before, snapshot(t, f.claude))

	// local/ には複製が入り、kata.yml に載っていること。
	body, err := os.ReadFile(filepath.Join(f.repo, "local", "skills", "my-review", "SKILL.md"))
	if err != nil || string(body) != "# my review\n" {
		t.Fatalf("copied skill = %q, %v", body, err)
	}
	m, err := manifest.Load(f.cfg.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Packages) != 2 {
		t.Fatalf("manifest declares %d packages, want 2", len(m.Packages))
	}
	// 配置していないので state は書かれない。
	if _, err := os.Stat(f.cfg.StatePath()); err == nil {
		t.Fatal("import must not record a deployment")
	}
}

// TestImportSkipsKataManagedLinks は、kata が既に配置したものを二重に取り込まないことを
// 確かめる。
func TestImportSkipsKataManagedLinks(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}

	rep, err := f.open(t).Import(context.Background(), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	it, ok := itemsByName(rep)["a"]
	if !ok || it.Action != ImportSkipped {
		t.Fatalf("item = %+v, want it to be skipped", it)
	}
	if !strings.Contains(it.Reason, "managed by kata") {
		t.Fatalf("reason = %q, want it to explain that kata manages it", it.Reason)
	}
	if got := rep.Counts()[ImportImported]; got != 0 {
		t.Fatalf("imported = %d, want 0", got)
	}
}

// TestImportSkipsManagedLinksWithoutState は、state.json を失っていても symlink であること
// だけで除外されることを確かめる。記録に頼る判定が外れても安全性が落ちない構造の検証。
func TestImportSkipsManagedLinksWithoutState(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	// 記録だけを失わせる。配置は生きたまま。
	if err := os.Remove(f.cfg.StatePath()); err != nil {
		t.Fatal(err)
	}

	rep, err := f.open(t).Import(context.Background(), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	it := itemsByName(rep)["a"]
	if it.Action != ImportSkipped || it.Reason != "already a symlink" {
		t.Fatalf("item = %+v, want it skipped because it is a symlink", it)
	}
}

// TestImportSkipsForeignSymlinks は、他のツールが張った symlink も除外することを確かめる。
func TestImportSkipsForeignSymlinks(t *testing.T) {
	f := newFixture(t)
	elsewhere := filepath.Join(t.TempDir(), "somewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.claude, "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, "shared")); err != nil {
		t.Skip("symlinks are not available in this environment")
	}

	rep, err := f.open(t).Import(context.Background(), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	it := itemsByName(rep)["shared"]
	if it.Action != ImportSkipped || it.Reason != "already a symlink" {
		t.Fatalf("item = %+v, want it skipped because it is a symlink", it)
	}
}

// TestImportDoesNotOverwriteExistingLocalPath は、利用者のリポジトリを壊さないことを
// 確かめる。取り込み先に既に何かあれば、内容を 1 バイトも変えてはならない。
func TestImportDoesNotOverwriteExistingLocalPath(t *testing.T) {
	f := newFixture(t)
	f.addClaudeSkill(t, "dup", "from claude\n")
	// 同名の実体が既に local/ にある。
	f.addSkill(t, "dup", "mine\n")
	before := snapshot(t, filepath.Join(f.repo, "local"))

	rep, err := f.open(t).Import(context.Background(), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	it := itemsByName(rep)["dup"]
	if it.Action != ImportSkipped || !strings.Contains(it.Reason, "already exists") {
		t.Fatalf("item = %+v, want it skipped because the local path is taken", it)
	}
	assertUnchanged(t, "local directory", before, snapshot(t, filepath.Join(f.repo, "local")))
}

// TestImportDoesNotOverwriteExistingDeclaration は、既にある宣言を書き換えないことを
// 確かめる。
func TestImportDoesNotOverwriteExistingDeclaration(t *testing.T) {
	f := newFixture(t)
	f.declare(t, manifest.Package{
		Name: "pdf", Type: manifest.TypeSkill,
		Git: "https://github.com/anthropics/skills", Path: "skills/pdf",
	})
	f.addClaudeSkill(t, "pdf", "handmade\n")

	before, err := manifest.Load(f.cfg.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := f.open(t).Import(context.Background(), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	it := itemsByName(rep)["pdf"]
	if it.Action != ImportSkipped || !strings.Contains(it.Reason, "already declared") {
		t.Fatalf("item = %+v, want it skipped because it is already declared", it)
	}

	after, err := manifest.Load(f.cfg.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := after.Find("pdf")
	want, _ := before.Find("pdf")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("declaration changed from %+v to %+v", want, got)
	}
}

// TestImportDryRunWritesNothing は、dry-run が何も書かないことと、
// そのとき示す local: の値が本番の書き込みと一致することを確かめる。
func TestImportDryRunWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.addClaudeSkill(t, "my-review", "# my review\n")

	manifestBefore, err := os.ReadFile(f.cfg.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	localBefore := snapshot(t, filepath.Join(f.repo, "local"))

	dry, err := f.open(t).Import(context.Background(), ImportOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := dry.Counts()[ImportImported]; got != 1 {
		t.Fatalf("planned imports = %d, want 1", got)
	}
	manifestAfter, err := os.ReadFile(f.cfg.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(manifestBefore) != string(manifestAfter) {
		t.Fatal("dry run must not write the manifest")
	}
	assertUnchanged(t, "local directory", localBefore, snapshot(t, filepath.Join(f.repo, "local")))

	// 本番で実際に書かれる値と、dry-run が示した値が一致すること。
	real, err := f.open(t).Import(context.Background(), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := itemsByName(real)["my-review"].Local, itemsByName(dry)["my-review"].Local; got != want {
		t.Fatalf("local value = %q after the real run, but the dry run showed %q", got, want)
	}
	m, err := manifest.Load(f.cfg.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := m.Find("my-review")
	if p.Local != itemsByName(dry)["my-review"].Local {
		t.Fatalf("manifest has local %q, but the dry run showed %q", p.Local, itemsByName(dry)["my-review"].Local)
	}
}

// TestImportAlwaysLeavesManifestLoadable は、配置先に何が置かれていても
// 取り込み後の kata.yml が読めることを確かめる。
// 読めない kata.yml を書くと全コマンドが動かなくなり、手で直すしかなくなる。
func TestImportAlwaysLeavesManifestLoadable(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.claude, "skills")
	for _, name := range []string{"My Skill", "..weird", "-leading", "UPPER", "ok-one"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := f.open(t).Import(context.Background(), ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Load(f.cfg.ManifestPath)
	if err != nil {
		t.Fatalf("manifest became unloadable after import: %v", err)
	}
	// 使える名前だけが載る。
	if len(m.Packages) != 1 || m.Packages[0].Name != "ok-one" {
		t.Fatalf("packages = %+v, want only ok-one", m.Packages)
	}
}

// TestImportIsIdempotent は、2 回目の import が何も取り込まないことを確かめる。
func TestImportIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.addClaudeSkill(t, "a", "x")

	if _, err := f.open(t).Import(context.Background(), ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	manifestAfterFirst, err := os.ReadFile(f.cfg.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}

	rep, err := f.open(t).Import(context.Background(), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Counts()[ImportImported]; got != 0 {
		t.Fatalf("second run imported %d, want 0", got)
	}
	manifestAfterSecond, err := os.ReadFile(f.cfg.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(manifestAfterFirst) != string(manifestAfterSecond) {
		t.Fatal("a second import must not change the manifest")
	}
}

// TestImportAdoptBacksUpAndLinks は、--adopt が元を退避してから配置先を
// kata の管理下へ置き換えることを確かめる。
func TestImportAdoptBacksUpAndLinks(t *testing.T) {
	f := newFixture(t)
	f.addClaudeSkill(t, "one", "first\n")
	f.addClaudeSkill(t, "two", "second\n")

	rep, err := f.open(t).Import(context.Background(), ImportOptions{Adopt: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Counts()[ImportImported]; got != 2 {
		t.Fatalf("imported = %d, want 2 (items: %+v)", got, rep.Items)
	}

	for _, name := range []string{"one", "two"} {
		assertLink(t,
			filepath.Join(f.claude, "skills", name),
			filepath.Join(f.repo, "local", "skills", name))
	}
	// リンク越しに元の内容が読めること。
	body, err := os.ReadFile(filepath.Join(f.claude, "skills", "one", "SKILL.md"))
	if err != nil || string(body) != "first\n" {
		t.Fatalf("content through the link = %q, %v", body, err)
	}

	// 退避物が 1 つのディレクトリにまとまっていること。
	backups := filepath.Join(f.cfg.KataHome, "backups")
	stamps, err := os.ReadDir(backups)
	if err != nil {
		t.Fatal(err)
	}
	if len(stamps) != 1 {
		t.Fatalf("backup directories = %d, want all of one run in a single directory", len(stamps))
	}
	for _, name := range []string{"one", "two"} {
		p := filepath.Join(backups, stamps[0].Name(), name, "SKILL.md")
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("original %s was not backed up: %v", name, err)
		}
	}
}

// TestImportAdoptDoesNotTouchOtherPackages は、--adopt の退避が取り込み対象だけに
// 及ぶことを確かめる。Sync(Force) に丸投げしていると、無関係なパッケージの配置先に
// ある利用者のファイルまで退避してしまう。
func TestImportAdoptDoesNotTouchOtherPackages(t *testing.T) {
	f := newFixture(t)
	// 宣言済みだが、配置先は利用者の実ファイルで占有されている。
	f.addSkill(t, "declared", "source\n")
	f.declare(t, manifest.Package{
		Name: "declared", Type: manifest.TypeSkill, Local: "./local/skills/declared",
	})
	occupied := f.addClaudeSkill(t, "declared", "user data\n")

	// 取り込む対象は別のもの。
	f.addClaudeSkill(t, "fresh", "fresh\n")

	if _, err := f.open(t).Import(context.Background(), ImportOptions{Adopt: true}); err != nil {
		t.Fatal(err)
	}

	// 取り込み対象外の実ファイルは無傷でなければならない。
	body, err := os.ReadFile(filepath.Join(occupied, "SKILL.md"))
	if err != nil || string(body) != "user data\n" {
		t.Fatalf("an unrelated user file was disturbed: %q, %v", body, err)
	}
	// 取り込んだほうは置き換わっている。
	assertLink(t, filepath.Join(f.claude, "skills", "fresh"),
		filepath.Join(f.repo, "local", "skills", "fresh"))
}

// TestImportAdoptThenSyncIsUnchanged は、--adopt の直後の sync が何もしないことを
// 確かめる。配置の記録が正しく残っていなければ冪等にならない。
func TestImportAdoptThenSyncIsUnchanged(t *testing.T) {
	f := newFixture(t)
	f.addClaudeSkill(t, "a", "x\n")

	if _, err := f.open(t).Import(context.Background(), ImportOptions{Adopt: true}); err != nil {
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

// TestImportTypeFilter は --type による絞り込みを確かめる。
func TestImportTypeFilter(t *testing.T) {
	f := newFixture(t)
	f.addClaudeSkill(t, "a", "x")
	f.addClaudeCommand(t, "pr", "# pr\n")

	rep, err := f.open(t).Import(context.Background(), ImportOptions{
		Types: []string{manifest.TypeCommand},
	})
	if err != nil {
		t.Fatal(err)
	}
	names := itemsByName(rep)
	if _, ok := names["a"]; ok {
		t.Fatal("the skill must not be considered when only commands are requested")
	}
	if names["pr"].Action != ImportImported {
		t.Fatalf("pr = %+v, want it imported", names["pr"])
	}
}

// TestImportNameFilter は名前による絞り込みを確かめる。
func TestImportNameFilter(t *testing.T) {
	f := newFixture(t)
	f.addClaudeSkill(t, "wanted", "x")
	f.addClaudeSkill(t, "other", "y")

	rep, err := f.open(t).Import(context.Background(), ImportOptions{Names: []string{"wanted"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 1 || rep.Items[0].Name != "wanted" {
		t.Fatalf("items = %+v, want only wanted", rep.Items)
	}
}

// TestImportIgnoresHiddenEntries は、隠しファイルを候補にしないことを確かめる。
// macOS の .DS_Store を毎回「読み飛ばした」と報告しても意味がない。
func TestImportIgnoresHiddenEntries(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(f.claude, "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := f.open(t).Import(context.Background(), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 0 {
		t.Fatalf("items = %+v, want hidden entries to be ignored entirely", rep.Items)
	}
}
