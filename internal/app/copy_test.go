package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cutmail/kata/internal/linker"
	"github.com/cutmail/kata/internal/manifest"
)

// copyFixture は copy 戦略で 1 件を宣言し、一度配置する。
func copyFixture(t *testing.T) (fixture, string) {
	t.Helper()
	f := newFixture(t)
	f.addSkill(t, "a", "v1\n")
	f.declare(t, manifest.Package{
		Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a",
		Strategy: manifest.StrategyCopy,
	})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	return f, filepath.Join(f.claude, "skills", "a")
}

func TestSyncCopyStrategyDeploysRealFiles(t *testing.T) {
	f, dest := copyFixture(t)

	fi, err := os.Lstat(dest)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("destination should be a real directory: %v, %v", fi, err)
	}
	body, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil || string(body) != "v1\n" {
		t.Fatalf("deployed content = %q, %v", body, err)
	}
	// 複製元には手を出さない。
	src, err := os.ReadFile(filepath.Join(f.repo, "local", "skills", "a", "SKILL.md"))
	if err != nil || string(src) != "v1\n" {
		t.Fatalf("the source was disturbed: %q, %v", src, err)
	}
}

// TestSyncCopyIsIdempotent は 2 回目の sync が何もしないことを確かめる。
// ここが崩れると、権限の正規化規則がどこかでずれている合図になる。
func TestSyncCopyIsIdempotent(t *testing.T) {
	f, _ := copyFixture(t)

	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Counts()[ActionUnchanged]; got != 1 {
		t.Fatalf("unchanged = %d, want 1 (counts: %v)", got, rep.Counts())
	}
}

// TestListReportsCopiedStatus は、copy 戦略の配置が実体であっても
// 「宣言どおり」と判定されることを確かめる。
func TestListReportsCopiedStatus(t *testing.T) {
	f, dest := copyFixture(t)

	items, err := f.open(t).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != StatusCopied {
		t.Fatalf("items = %+v, want a single copied entry", items)
	}
	if !items[0].Status.Deployed() {
		t.Fatal("a copied deployment must count as deployed")
	}

	// 編集するとズレとして現れる。
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err = f.open(t).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Status != StatusDrifted {
		t.Fatalf("status = %s, want drifted after an edit", items[0].Status)
	}
}

// TestSyncCopyLeavesEditedDeployment は、配置先を編集したあとの sync が
// 失敗として報告し、編集を残すことを確かめる。
func TestSyncCopyLeavesEditedDeployment(t *testing.T) {
	f, dest := copyFixture(t)

	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte("my edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 取得元も進める。
	if err := os.WriteFile(filepath.Join(f.repo, "local", "skills", "a", "SKILL.md"),
		[]byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err == nil {
		t.Fatal("expected sync to fail on an edited copy")
	}
	if !errors.Is(rep.Changes[0].Err, linker.ErrModified) {
		t.Fatalf("err = %v, want ErrModified", rep.Changes[0].Err)
	}
	body, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil || string(body) != "my edit\n" {
		t.Fatalf("the edit was lost: %q, %v", body, err)
	}
}

// TestSyncCopyUpdatesUneditedDeployment は、手が入っていなければ
// 取得元の変更が反映されることを確かめる。
func TestSyncCopyUpdatesUneditedDeployment(t *testing.T) {
	f, dest := copyFixture(t)

	if err := os.WriteFile(filepath.Join(f.repo, "local", "skills", "a", "SKILL.md"),
		[]byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Counts()[ActionUpdate]; got != 1 {
		t.Fatalf("updated = %d, want 1 (counts: %v)", got, rep.Counts())
	}
	body, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil || string(body) != "v2\n" {
		t.Fatalf("deployed content = %q, %v; want v2", body, err)
	}
}

// TestRemoveKeepsEditedCopy は、撤去のときも編集を消さないことを確かめる。
func TestRemoveKeepsEditedCopy(t *testing.T) {
	f, dest := copyFixture(t)

	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte("my edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := f.open(t).Remove(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if res.Unlinked {
		t.Fatal("an edited copy must not be removed")
	}
	if res.Warning == "" {
		t.Fatal("expected the caller to be told that the copy was left in place")
	}
	body, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil || string(body) != "my edit\n" {
		t.Fatalf("the edit was lost: %q, %v", body, err)
	}
}

// TestRemoveDeletesUneditedCopy は、手が入っていない配置は撤去されることを確かめる。
func TestRemoveDeletesUneditedCopy(t *testing.T) {
	f, dest := copyFixture(t)

	res, err := f.open(t).Remove(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Unlinked {
		t.Fatal("an untouched copy should be removed")
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatal("the deployed copy should be gone")
	}
	// 実体は消してはならない。
	if _, err := os.Stat(filepath.Join(f.repo, "local", "skills", "a", "SKILL.md")); err != nil {
		t.Fatal("the source must be left untouched")
	}
}

// TestSyncCopyRefusesUnmanagedDestination は、手で置いたものを上書きしないことを
// 確かめる。link 戦略と同じ保証が copy にもあること。
func TestSyncCopyRefusesUnmanagedDestination(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "from kata\n")
	f.declare(t, manifest.Package{
		Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a",
		Strategy: manifest.StrategyCopy,
	})
	dest := f.addClaudeSkill(t, "a", "user data\n")

	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err == nil {
		t.Fatal("expected sync to fail")
	}
	if !errors.Is(rep.Changes[0].Err, linker.ErrOccupied) {
		t.Fatalf("err = %v, want ErrOccupied", rep.Changes[0].Err)
	}
	body, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil || string(body) != "user data\n" {
		t.Fatalf("existing content was damaged: %q, %v", body, err)
	}
}

// TestSyncAutoStrategyRecordsResolvedValue は、auto が解決後の戦略を記録することを
// 確かめる。記録が auto のままだと、撤去がどちらの経路を通るか決められない。
func TestSyncAutoStrategyRecordsResolvedValue(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{
		Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a",
		Strategy: manifest.StrategyAuto,
	})

	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	a := f.open(t)
	e, ok := a.st.Get(a.Origin(), "a")
	if !ok {
		t.Fatal("expected a deployment record")
	}
	if e.Strategy == manifest.StrategyAuto {
		t.Fatal("the recorded strategy must be the resolved one, not auto")
	}
	if e.Strategy != manifest.StrategyLink && e.Strategy != manifest.StrategyCopy {
		t.Fatalf("recorded strategy = %q, want link or copy", e.Strategy)
	}
}
