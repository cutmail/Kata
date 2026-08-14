package app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cutmail/kata/internal/lockfile"
	"github.com/cutmail/kata/internal/manifest"
)

// findCheck は名前と本文で診断項目を探す。
func findCheck(rep *DoctorReport, name, contains string) (Check, bool) {
	for _, c := range rep.Checks {
		if c.Name == name && strings.Contains(c.Detail, contains) {
			return c, true
		}
	}
	return Check{}, false
}

func TestDoctorPassesOnCleanSetup(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}

	rep, err := Diagnose(context.Background(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Worst() != LevelOK {
		t.Fatalf("worst = %s, want ok (checks: %+v)", rep.Worst(), rep.Checks)
	}
}

// TestDoctorWritesNothing は、診断が状態を変えないことを確かめる。
// 壊れているときに使う道具が、それ自体で状況を変えてはならない。
func TestDoctorWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}

	claudeBefore := snapshot(t, f.claude)
	repoBefore := snapshot(t, f.repo)

	if _, err := Diagnose(context.Background(), f.cfg); err != nil {
		t.Fatal(err)
	}
	// 探索用のファイルが残っていないこと。
	assertUnchanged(t, "deployment target", claudeBefore, snapshot(t, f.claude))
	assertUnchanged(t, "manifest repository", repoBefore, snapshot(t, f.repo))
}

// TestDoctorDoesNotCreateTheTargetDirectory は、配置先が無いときに
// 診断が勝手に作らないことを確かめる。
func TestDoctorDoesNotCreateTheTargetDirectory(t *testing.T) {
	f := newFixture(t)
	if _, err := os.Stat(f.claude); !os.IsNotExist(err) {
		t.Skip("the fixture already created the target directory")
	}
	if _, err := Diagnose(context.Background(), f.cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.claude); !os.IsNotExist(err) {
		t.Fatal("doctor must not create the deployment target just to inspect it")
	}
}

// TestDoctorDetectsLockRefDrift は、kata.yml の ref を書き換えても sync が
// 黙って無視することを、診断が事前に説明できることを確かめる。
func TestDoctorDetectsLockRefDrift(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{
		Name: "a", Type: manifest.TypeSkill,
		Git: "https://example.com/repo", Ref: "v2",
	})

	a := f.open(t)
	a.lock.Put(lockfile.Entry{
		Name: "a", Type: manifest.TypeSkill,
		Source: "git+https://example.com/repo", Ref: "main", Commit: "1111111111111111111111111111111111111111",
	})
	if err := a.lock.Save(f.cfg.LockPath(), time.Now()); err != nil {
		t.Fatal(err)
	}

	rep, err := Diagnose(context.Background(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCheck(rep, "lock-consistency", "declares ref")
	if !ok {
		t.Fatalf("expected the ref mismatch to be reported, checks = %+v", rep.Checks)
	}
	if !strings.Contains(c.Hint, "kata update") {
		t.Fatalf("hint = %q, want it to point at 'kata update'", c.Hint)
	}
}

// TestDoctorDetectsLockSourceDrift は、取得元を書き換えたときに sync が
// 分かりにくいエラーで落ちることを、診断が先に説明できることを確かめる。
func TestDoctorDetectsLockSourceDrift(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{
		Name: "a", Type: manifest.TypeSkill, Git: "https://example.com/new",
	})

	a := f.open(t)
	a.lock.Put(lockfile.Entry{
		Name: "a", Type: manifest.TypeSkill,
		Source: "git+https://example.com/old", Commit: "1111111111111111111111111111111111111111",
	})
	if err := a.lock.Save(f.cfg.LockPath(), time.Now()); err != nil {
		t.Fatal(err)
	}

	rep, err := Diagnose(context.Background(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findCheck(rep, "lock-consistency", "source changed"); !ok {
		t.Fatalf("expected the source change to be reported, checks = %+v", rep.Checks)
	}
}

// TestDoctorDetectsMissingState は、state.json を失っただけの状況を
// 「全部ズレている」ではなく原因として説明できることを確かめる。
func TestDoctorDetectsMissingState(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.cfg.StatePath()); err != nil {
		t.Fatal(err)
	}

	rep, err := Diagnose(context.Background(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCheck(rep, "state-integrity", "is missing")
	if !ok {
		t.Fatalf("expected the missing state file to be named, checks = %+v", rep.Checks)
	}
	if !strings.Contains(c.Hint, "kata sync") {
		t.Fatalf("hint = %q, want it to point at 'kata sync'", c.Hint)
	}
}

// TestDoctorReportsBrokenDeploymentAsError は、リンク先を失った配置が
// 警告ではなくエラーとして扱われることを確かめる。
func TestDoctorReportsBrokenDeploymentAsError(t *testing.T) {
	f := newFixture(t)
	src := f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}

	rep, err := Diagnose(context.Background(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Worst() != LevelError {
		t.Fatalf("worst = %s, want error (checks: %+v)", rep.Worst(), rep.Checks)
	}
}

// TestDoctorWithoutManifest は、kata.yml が無くても診断が走ることを確かめる。
func TestDoctorWithoutManifest(t *testing.T) {
	f := newFixture(t)
	cfg := f.cfg
	cfg.ManifestPath = ""

	rep, err := Diagnose(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCheck(rep, "manifest", "no "+manifest.FileName)
	if !ok {
		t.Fatalf("expected the missing manifest to be reported, checks = %+v", rep.Checks)
	}
	// 実行できない理由ではなく、直せる診断結果として扱う。
	if c.Level != LevelWarn {
		t.Fatalf("level = %s, want warn", c.Level)
	}
}

// TestDoctorReportsUnreadableManifest は、壊れた kata.yml をエラーとして
// 報告することを確かめる。
func TestDoctorReportsUnreadableManifest(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.cfg.ManifestPath, []byte("version: [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := Diagnose(context.Background(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Worst() != LevelError {
		t.Fatalf("worst = %s, want error", rep.Worst())
	}
}
