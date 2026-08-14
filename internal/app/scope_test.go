package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cutmail/kata/internal/manifest"
)

// projectHome はこの fixture の project スコープの配置先を返す。
func (f fixture) projectHome() string { return filepath.Join(f.repo, ".claude") }

func TestSyncProjectScopeDeploysNextToTheManifest(t *testing.T) {
	f := newFixture(t)
	src := f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{
		Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a",
		Scope: manifest.ScopeProject,
	})

	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	assertLink(t, filepath.Join(f.projectHome(), "skills", "a"), src)
}

// TestSyncProjectScopeDoesNotTouchUserHome は、project スコープの配置が
// 利用者の設定ディレクトリへ漏れないことを確かめる。
func TestSyncProjectScopeDoesNotTouchUserHome(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{
		Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a",
		Scope: manifest.ScopeProject,
	})

	before := snapshot(t, f.claude)
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	assertUnchanged(t, "user home", before, snapshot(t, f.claude))
}

// TestSyncProjectScopeLeavesOtherDotClaudeFiles は、プロジェクトの .claude に
// もともとある設定ファイルを壊さないことを確かめる。
// 配置先がリポジトリの中にあると、利用者の持ち物と同居することになる。
func TestSyncProjectScopeLeavesOtherDotClaudeFiles(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{
		Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a",
		Scope: manifest.ScopeProject,
	})

	settings := filepath.Join(f.projectHome(), "settings.json")
	if err := os.MkdirAll(f.projectHome(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"mine":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.open(t).Remove(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(settings)
	if err != nil || string(body) != `{"mine":true}` {
		t.Fatalf("settings.json = %q, %v; it must survive untouched", body, err)
	}
}

// TestSyncMovesDeploymentWhenScopeChanges は、scope を変えたときに古い配置が
// 撤去されることを確かめる。
//
// 撤去し損ねると、記録は新しい配置先に上書きされるため、古い方は誰にも撤去できない
// 孤児として残る。scope の変更は project スコープの導入で日常操作になる。
func TestSyncMovesDeploymentWhenScopeChanges(t *testing.T) {
	f := newFixture(t)
	src := f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"})

	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	userDest := filepath.Join(f.claude, "skills", "a")
	assertLink(t, userDest, src)

	// scope を project へ移す。
	a := f.open(t)
	for i := range a.man.Packages {
		if a.man.Packages[i].Name == "a" {
			a.man.Packages[i].Scope = manifest.ScopeProject
		}
	}
	if err := a.man.Save(f.cfg.ManifestPath); err != nil {
		t.Fatal(err)
	}

	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// 新しい配置先にある。
	assertLink(t, filepath.Join(f.projectHome(), "skills", "a"), src)
	// 古い配置先には何も残っていない。
	if _, err := os.Lstat(userDest); !os.IsNotExist(err) {
		t.Fatalf("the previous deployment at %s was left behind", userDest)
	}
	if rep.Changes[0].Warning == "" {
		t.Fatal("expected the move to be reported")
	}
}

// TestSyncMovesDeploymentWhenTypeChanges は、type を変えたときも同じ後始末が
// 効くことを確かめる。scope と同じ穴が type にもある。
func TestSyncMovesDeploymentWhenTypeChanges(t *testing.T) {
	f := newFixture(t)
	cmdPath := f.addCommand(t, "note")
	f.declare(t, manifest.Package{
		Name: "note", Type: manifest.TypeCommand, Local: "./local/commands/note.md",
	})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	commandDest := filepath.Join(f.claude, "commands", "note.md")
	assertLink(t, commandDest, cmdPath)

	// 同じ実体を agent として宣言し直す。
	a := f.open(t)
	for i := range a.man.Packages {
		if a.man.Packages[i].Name == "note" {
			a.man.Packages[i].Type = manifest.TypeAgent
		}
	}
	if err := a.man.Save(f.cfg.ManifestPath); err != nil {
		t.Fatal(err)
	}

	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	assertLink(t, filepath.Join(f.claude, "agents", "note.md"), cmdPath)
	if _, err := os.Lstat(commandDest); !os.IsNotExist(err) {
		t.Fatalf("the previous deployment at %s was left behind", commandDest)
	}
}

// TestManifestRejectsUnknownScope は、知らないスコープを宣言できないことを確かめる。
func TestManifestRejectsUnknownScope(t *testing.T) {
	f := newFixture(t)
	a := f.open(t)
	if err := a.man.Add(manifest.Package{
		Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a",
		Scope: "machine", Strategy: manifest.StrategyLink,
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.man.Validate(); err == nil {
		t.Fatal("expected an unknown scope to be rejected")
	}
}
