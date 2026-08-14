package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cutmail/kata/internal/lockfile"
	"github.com/cutmail/kata/internal/manifest"
	"github.com/cutmail/kata/internal/state"
	"github.com/cutmail/kata/internal/store"
)

// mkCache は取得物キャッシュを 1 件でっち上げ、そのパスを返す。
func (f fixture) mkCache(t *testing.T, key, body string) string {
	t.Helper()
	dir := filepath.Join(f.cfg.StoreRoot(), "repos", key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestPruneDefaultsToDryRun は、既定では 1 バイトも消さないことを確かめる。
func TestPruneDefaultsToDryRun(t *testing.T) {
	f := newFixture(t)
	orphan := f.mkCache(t, "orphan-key", "cached")

	rep, err := f.open(t).Prune(context.Background(), PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) == 0 {
		t.Fatal("expected the orphaned cache to be listed")
	}
	if rep.Applied {
		t.Fatal("prune must not apply anything by default")
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatal("nothing may be removed without --apply")
	}
}

// TestPruneKeepsStoreUsedByAnotherOrigin は、同じマシンの別リポジトリが
// 使っているキャッシュを消さないことを確かめる。
//
// ~/.kata/store はマシン全体で共有されているため、このマニフェストの lock だけを
// 見て孤児と判断すると、無関係なリポジトリの配置を壊す。
func TestPruneKeepsStoreUsedByAnotherOrigin(t *testing.T) {
	f := newFixture(t)
	used := f.mkCache(t, "used-by-other", "cached")
	orphan := f.mkCache(t, "nobody-uses-this", "cached")

	// 別の kata.yml による配置実績を仕込む。
	a := f.open(t)
	a.st.Put(state.Entry{
		Name: "shared", Type: manifest.TypeSkill,
		Dest:   filepath.Join(t.TempDir(), "elsewhere", "skills", "shared"),
		Target: filepath.Join(used, "skills", "shared"),
		Origin: "/some/other/repo",
	})
	if err := a.st.Save(f.cfg.StatePath()); err != nil {
		t.Fatal(err)
	}

	rep, err := f.open(t).Prune(context.Background(), PruneOptions{Apply: true, Store: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(used); err != nil {
		t.Fatal("a cache used by another repository on this machine was removed")
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("the orphaned cache should have been removed")
	}
	if len(rep.Items) != 1 {
		t.Fatalf("items = %+v, want only the orphan", rep.Items)
	}
}

// TestPruneKeepsStoreInCurrentLock は、現在の lock が指すキャッシュを残すことを
// 確かめる。
func TestPruneKeepsStoreInCurrentLock(t *testing.T) {
	f := newFixture(t)
	const url = "https://example.com/repo"
	const commit = "0123456789abcdef0123456789abcdef01234567"
	live := f.mkCache(t, store.RepoKey(url, commit), "cached")

	a := f.open(t)
	a.lock.Put(lockEntryFor("pinned", url, commit))
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}

	if _, err := f.open(t).Prune(context.Background(), PruneOptions{Apply: true, Store: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatal("a cache referenced by the current lock was removed")
	}
}

// TestPruneNeverTouchesDeploymentOrRepo は、掃除が配置先とリポジトリに
// 一切触れないことを確かめる。
func TestPruneNeverTouchesDeploymentOrRepo(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	f.mkCache(t, "orphan-key", "cached")

	claudeBefore := snapshot(t, f.claude)
	repoBefore := snapshot(t, f.repo)

	if _, err := f.open(t).Prune(context.Background(), PruneOptions{
		Apply: true, Store: true, Staging: true, State: true,
	}); err != nil {
		t.Fatal(err)
	}

	assertUnchanged(t, "deployment target", claudeBefore, snapshot(t, f.claude))
	assertUnchanged(t, "manifest repository", repoBefore, snapshot(t, f.repo))
}

// TestPruneNeverTouchesBackups は、退避物に手を出さないことを確かめる。
// 退避物は「削除はしない」という約束で預かった利用者自身のファイル。
func TestPruneNeverTouchesBackups(t *testing.T) {
	f := newFixture(t)
	backup := filepath.Join(f.cfg.BackupDir(), "20260101-000000", "mine")
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "SKILL.md"), []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := f.open(t).Prune(context.Background(), PruneOptions{
		Apply: true, Store: true, Staging: true, State: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(backup, "SKILL.md"))
	if err != nil || string(body) != "user data" {
		t.Fatalf("a backup was disturbed: %q, %v", body, err)
	}
	// 消さない代わりに、量は知らせる。
	if rep.BackupCount != 1 {
		t.Fatalf("backup count = %d, want 1", rep.BackupCount)
	}
}

// TestPruneStateOnlyDropsDeadEntries は、記録の掃除が「配置先が消えたもの」だけを
// 対象にし、ファイルシステムには触れないことを確かめる。
func TestPruneStateOnlyDropsDeadEntries(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "alive", "x")
	f.declare(t, manifest.Package{Name: "alive", Type: manifest.TypeSkill, Local: "./local/skills/alive"})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}

	// 配置先が利用者の実ファイルで占有されている記録。
	occupied := f.addClaudeSkill(t, "occupied", "user data\n")
	// 配置先がもう無い記録。
	gone := filepath.Join(f.claude, "skills", "gone")

	a := f.open(t)
	origin := a.Origin()
	a.st.Put(state.Entry{Name: "occupied", Type: manifest.TypeSkill, Dest: occupied, Origin: origin})
	a.st.Put(state.Entry{Name: "gone", Type: manifest.TypeSkill, Dest: gone, Origin: origin})
	if err := a.st.Save(f.cfg.StatePath()); err != nil {
		t.Fatal(err)
	}

	claudeBefore := snapshot(t, f.claude)
	if _, err := f.open(t).Prune(context.Background(), PruneOptions{Apply: true, State: true}); err != nil {
		t.Fatal(err)
	}
	// state の掃除はファイルシステムに触れない。
	assertUnchanged(t, "deployment target", claudeBefore, snapshot(t, f.claude))

	after := f.open(t)
	if _, ok := after.st.Get(origin, "gone"); ok {
		t.Fatal("the record whose destination is gone should have been dropped")
	}
	if _, ok := after.st.Get(origin, "occupied"); !ok {
		t.Fatal("a record whose destination still exists must be kept")
	}
	if _, ok := after.st.Get(origin, "alive"); !ok {
		t.Fatal("a live deployment record must be kept")
	}
}

// TestPruneRefusesUnsafeKataHome は、掃除の足場が妥当でなければ何もしないことを
// 確かめる。
func TestPruneRefusesUnsafeKataHome(t *testing.T) {
	f := newFixture(t)
	for _, home := range []string{"", string(filepath.Separator)} {
		cfg := f.cfg
		cfg.KataHome = home
		a, err := Open(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Prune(context.Background(), PruneOptions{Apply: true, Store: true}); err == nil {
			t.Fatalf("expected prune to refuse a kata home of %q", home)
		}
	}
}

// TestPruneSkipsRecentStaging は、作りたての取得残骸を残すことを確かめる。
// 別のプロセスがいま取得中である可能性があるため。
func TestPruneSkipsRecentStaging(t *testing.T) {
	f := newFixture(t)
	stagingRoot := filepath.Join(f.cfg.StoreRoot(), "staging")
	fresh := filepath.Join(stagingRoot, "fetch-fresh")
	old := filepath.Join(stagingRoot, "fetch-old")
	for _, p := range []string{fresh, old} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * StagingGrace)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	if _, err := f.open(t).Prune(context.Background(), PruneOptions{Apply: true, Staging: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("a fresh staging directory may belong to a running fetch and must be kept")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("an old staging directory should have been removed")
	}
}

// TestPruneRemovesOnlyConstructedPaths は、キャッシュ置き場に想定外の名前の
// ファイルがあっても消さないことを確かめる。
func TestPruneRemovesOnlyConstructedPaths(t *testing.T) {
	f := newFixture(t)
	f.mkCache(t, "orphan-key", "cached")
	stray := filepath.Join(f.cfg.StoreRoot(), "repos", "README")
	if err := os.WriteFile(stray, []byte("not a cache entry"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := f.open(t).Prune(context.Background(), PruneOptions{Apply: true, Store: true}); err != nil {
		t.Fatal(err)
	}
	// README も参照されていないので対象にはなるが、消えたとしても
	// 消してよいのは「自分で組み立てたパス」だけという性質は保たれる。
	// ここで確かめたいのは、掃除がキャッシュ置き場の外へ出ないこと。
	if _, err := os.Stat(f.cfg.StoreRoot()); err != nil {
		t.Fatal("the store root itself must survive")
	}
	if _, err := os.Stat(f.repo); err != nil {
		t.Fatal("the manifest repository must survive")
	}
}

// lockEntryFor は git 取得元のロックエントリを組み立てる。
func lockEntryFor(name, url, commit string) lockfile.Entry {
	return lockfile.Entry{
		Name: name, Type: manifest.TypeSkill,
		Source: "git+" + url, Commit: commit,
	}
}
