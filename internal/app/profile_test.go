package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/cutmail/kata/internal/lockfile"
	"github.com/cutmail/kata/internal/manifest"
)

// twoProfiles は work と home に分かれた 2 件を宣言し、一度配置する。
func twoProfiles(t *testing.T) fixture {
	t.Helper()
	f := newFixture(t)
	f.addSkill(t, "work-only", "w")
	f.addSkill(t, "home-only", "h")
	f.declare(t,
		manifest.Package{
			Name: "work-only", Type: manifest.TypeSkill, Local: "./local/skills/work-only",
			Profiles: []string{"work"},
		},
		manifest.Package{
			Name: "home-only", Type: manifest.TypeSkill, Local: "./local/skills/home-only",
			Profiles: []string{"home"},
		},
	)
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	return f
}

// TestSyncWithProfileKeepsLockEntriesForDeselected は、profile で絞った sync が
// 選外パッケージのコミットのピンを消さないことを確かめる。
//
// ここを取り違えると、一度 --profile 付きで sync して kata.lock をコミットした時点で
// 選外パッケージのピンが恒久的に失われ、別マシンで同じツリーを復元できなくなる。
// しかもその症状は別マシンでしか現れないため、発見が極めて難しい。
func TestSyncWithProfileKeepsLockEntriesForDeselected(t *testing.T) {
	f := twoProfiles(t)

	// 選外になるほうのピンを、はっきり分かる値にしておく。
	lk, err := lockfile.Load(f.cfg.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	e, ok := lk.Get("home-only")
	if !ok {
		t.Fatal("home-only should have a lock entry after the first sync")
	}
	e.Commit = "0123456789abcdef0123456789abcdef01234567"
	lk.Put(e)
	if err := lk.Save(f.cfg.LockPath(), time.Now()); err != nil {
		t.Fatal(err)
	}
	want, _ := lk.Get("home-only")

	if _, err := f.open(t).Sync(context.Background(), SyncOptions{Profile: "work"}); err != nil {
		t.Fatal(err)
	}

	after, err := lockfile.Load(f.cfg.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	got, ok := after.Get("home-only")
	if !ok {
		t.Fatal("the lock entry for the deselected package was deleted; its commit pin is lost")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lock entry changed from %+v to %+v", want, got)
	}
}

// TestSyncWithProfileDoesNotUndeployDeselected は、絞り込みが「対象を限定する」
// 操作であって「選外を剥がす」操作ではないことを確かめる。
func TestSyncWithProfileDoesNotUndeployDeselected(t *testing.T) {
	f := twoProfiles(t)
	dest := filepath.Join(f.claude, "skills", "home-only")

	rep, err := f.open(t).Sync(context.Background(), SyncOptions{Profile: "work"})
	if err != nil {
		t.Fatal(err)
	}
	assertLink(t, dest, filepath.Join(f.repo, "local/skills/home-only"))

	// 残っていることは黙らせず知らせる。
	var warned bool
	for _, c := range rep.Changes {
		if c.Name == "home-only" && c.Warning != "" {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("expected the deselected deployment to be reported, changes = %+v", rep.Changes)
	}
}

// TestSyncPruneUndeploysDeselectedButKeepsLock は、--prune が配置だけを剥がし、
// ピンは残すことを確かめる。パッケージは宣言に残っているのだから、
// 再現に必要な情報を捨ててはならない。
func TestSyncPruneUndeploysDeselectedButKeepsLock(t *testing.T) {
	f := twoProfiles(t)
	dest := filepath.Join(f.claude, "skills", "home-only")

	if _, err := f.open(t).Sync(context.Background(), SyncOptions{Profile: "work", Prune: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatal("--prune should have undeployed the deselected package")
	}
	lk, err := lockfile.Load(f.cfg.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lk.Get("home-only"); !ok {
		t.Fatal("--prune must not drop the lock entry of a package that is still declared")
	}
	// 選ばれたほうは配置されたまま。
	assertLink(t, filepath.Join(f.claude, "skills", "work-only"),
		filepath.Join(f.repo, "local/skills/work-only"))
}

// TestSyncWithProfileStillUndeploysUndeclared は、profile を指定していても
// 宣言から消えたものは撤去されることを確かめる。冪等性は profile と無関係に保たれる。
func TestSyncWithProfileStillUndeploysUndeclared(t *testing.T) {
	f := twoProfiles(t)

	a := f.open(t)
	a.man.Remove("home-only")
	if err := a.man.Save(f.cfg.ManifestPath); err != nil {
		t.Fatal(err)
	}

	if _, err := f.open(t).Sync(context.Background(), SyncOptions{Profile: "work"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(f.claude, "skills", "home-only")); !os.IsNotExist(err) {
		t.Fatal("a package removed from the manifest must be undeployed even under a profile")
	}
	lk, err := lockfile.Load(f.cfg.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lk.Get("home-only"); ok {
		t.Fatal("the lock entry should be gone once the package is no longer declared")
	}
}

// TestSyncUnknownProfileChangesNothing は、打ち間違えた profile が
// 「該当なしの成功」にならないことを確かめる。
func TestSyncUnknownProfileChangesNothing(t *testing.T) {
	f := twoProfiles(t)
	claudeBefore := snapshot(t, f.claude)
	lockBefore, err := os.ReadFile(f.cfg.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	stateBefore, err := os.ReadFile(f.cfg.StatePath())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.open(t).Sync(context.Background(), SyncOptions{Profile: "wrok"}); err == nil {
		t.Fatal("expected an unknown profile to be rejected")
	}

	assertUnchanged(t, "deployment target", claudeBefore, snapshot(t, f.claude))
	lockAfter, _ := os.ReadFile(f.cfg.LockPath())
	if string(lockBefore) != string(lockAfter) {
		t.Fatal("the lock file must not change when the profile is rejected")
	}
	stateAfter, _ := os.ReadFile(f.cfg.StatePath())
	if string(stateBefore) != string(stateAfter) {
		t.Fatal("the state file must not change when the profile is rejected")
	}
}

// TestSyncWithoutProfileSelectsEverything は、profile を指定しなければ
// すべてが対象になることを確かめる。profiles を使わない利用者の挙動は変わらない。
func TestSyncWithoutProfileSelectsEverything(t *testing.T) {
	f := twoProfiles(t)
	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Counts()[ActionUnchanged]; got != 2 {
		t.Fatalf("unchanged = %d, want 2 (counts: %v)", got, rep.Counts())
	}
}

// TestSyncProfileSelectsUnprofiledPackages は、profiles を宣言していない
// パッケージがどの profile でも選ばれることを確かめる。
func TestSyncProfileSelectsUnprofiledPackages(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "always", "a")
	f.addSkill(t, "work-only", "w")
	f.declare(t,
		manifest.Package{Name: "always", Type: manifest.TypeSkill, Local: "./local/skills/always"},
		manifest.Package{
			Name: "work-only", Type: manifest.TypeSkill, Local: "./local/skills/work-only",
			Profiles: []string{"work"},
		},
	)

	if _, err := f.open(t).Sync(context.Background(), SyncOptions{Profile: "work"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"always", "work-only"} {
		assertLink(t, filepath.Join(f.claude, "skills", name),
			filepath.Join(f.repo, "local/skills", name))
	}
}

// TestManifestNormalizesProfiles は、書き出しの差分が安定するよう
// プロファイル名が整えられることを確かめる。
func TestManifestNormalizesProfiles(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{
		Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a",
		Profiles: []string{"work", " home ", "work", ""},
	})

	m, err := manifest.Load(f.cfg.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := m.Find("a")
	if !reflect.DeepEqual(p.Profiles, []string{"home", "work"}) {
		t.Fatalf("profiles = %v, want [home work]", p.Profiles)
	}
	if got := m.Profiles(); !reflect.DeepEqual(got, []string{"home", "work"}) {
		t.Fatalf("manifest profiles = %v, want [home work]", got)
	}
}

// TestManifestRejectsBadProfileName は、--profile と一致しえない表記を弾くことを
// 確かめる。黙って選択から外れるより、宣言の時点で気づけるほうがよい。
func TestManifestRejectsBadProfileName(t *testing.T) {
	f := newFixture(t)
	a := f.open(t)
	if err := a.man.Add(manifest.Package{
		Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a",
		Scope: manifest.ScopeUser, Strategy: manifest.StrategyLink,
		Profiles: []string{"Work Machine"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.man.Validate(); err == nil {
		t.Fatal("expected an unusable profile name to be rejected")
	}
}
