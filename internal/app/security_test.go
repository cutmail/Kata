package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cutmail/kata/internal/lockfile"
	"github.com/cutmail/kata/internal/manifest"
	"github.com/cutmail/kata/internal/state"
	"github.com/cutmail/kata/internal/store"
)

// linkOrSkip は symlink を作る。作れない環境ではテストを飛ばす。
func linkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are not available in this environment: %v", err)
	}
}

// TestProjectScopeCannotEscapeViaSymlink は、リポジトリにコミットされた
// .claude の symlink でリポジトリ外へ配置できないことを確かめる。
//
// 「設定リポジトリを clone して kata sync」という推奨どおりの手順で、
// 攻撃者が任意の場所へ書き込めてしまう経路だった。
func TestProjectScopeCannotEscapeViaSymlink(t *testing.T) {
	f := newFixture(t)
	outside := t.TempDir()
	f.addSkill(t, "pwn", "payload\n")
	linkOrSkip(t, outside, filepath.Join(f.repo, ".claude"))

	f.declare(t, manifest.Package{
		Name: "pwn", Type: manifest.TypeSkill, Local: "./local/skills/pwn",
		Scope: manifest.ScopeProject,
	})

	before := snapshot(t, outside)
	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err == nil {
		t.Fatal("expected the deployment to be refused")
	}
	if !strings.Contains(rep.Changes[0].Err.Error(), "symlink") {
		t.Fatalf("err = %v, want it to name the symlink", rep.Changes[0].Err)
	}
	assertUnchanged(t, "directory outside the repository", before, snapshot(t, outside))
}

// TestLocalSourceCannotEscapeViaSymlink は、local: がリポジトリ外を指す
// symlink であれば拒否することを確かめる。
func TestLocalSourceCannotEscapeViaSymlink(t *testing.T) {
	f := newFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.repo, "local", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkOrSkip(t, outside, filepath.Join(f.repo, "local", "skills", "leak"))

	f.declare(t, manifest.Package{
		Name: "leak", Type: manifest.TypeSkill, Local: "./local/skills/leak",
	})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err == nil {
		t.Fatal("expected a local source that leaves the repository to be refused")
	}
	if _, err := os.Stat(filepath.Join(f.claude, "skills", "leak", "secret")); err == nil {
		t.Fatal("the outside directory became readable through the skills directory")
	}
}

// TestLocalSourceTreeCannotContainSymlink は、取得物のツリーに symlink が
// 含まれていれば配置しないことを確かめる。
//
// 既定の link 戦略ではリンクがそのまま配置先に現れ、取得元が用意したリンク越しに
// ホームの任意の場所が「スキルの一部」として読めてしまう経路だった。
func TestLocalSourceTreeCannotContainSymlink(t *testing.T) {
	f := newFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := f.addSkill(t, "helper", "# helper\n")
	linkOrSkip(t, outside, filepath.Join(src, "reference"))

	f.declare(t, manifest.Package{
		Name: "helper", Type: manifest.TypeSkill, Local: "./local/skills/helper",
	})
	rep, err := f.open(t).Sync(context.Background(), SyncOptions{})
	if err == nil {
		t.Fatal("expected a tree containing a symlink to be refused")
	}
	if !strings.Contains(rep.Changes[0].Err.Error(), "symlink") {
		t.Fatalf("err = %v, want it to name the symlink", rep.Changes[0].Err)
	}
	if _, err := os.Stat(filepath.Join(f.claude, "skills", "helper")); err == nil {
		t.Fatal("nothing should have been deployed")
	}
}

// TestUndeployRefusesDestinationOutsideTheRoots は、記録された配置先が
// 配置ルートの外を指していても、そこを消しに行かないことを確かめる。
//
// state.json は平文で、壊れることも手で編集されることもある。
// 取り返しのつかない操作の根拠を 1 枚の記録だけに委ねない。
func TestUndeployRefusesDestinationOutsideTheRoots(t *testing.T) {
	f := newFixture(t)
	victim := filepath.Join(t.TempDir(), "important")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "notes.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := f.open(t)
	origin := a.Origin()
	// 宣言に無い名前で、配置ルートの外を指す記録を仕込む。
	a.st.Put(state.Entry{
		Name: "planted", Type: manifest.TypeSkill, Dest: victim,
		Strategy: manifest.StrategyCopy, Digest: "sha256:deadbeef", Origin: origin,
	})
	if err := a.st.Save(f.cfg.StatePath()); err != nil {
		t.Fatal(err)
	}

	// sync は「宣言から消えたもの」として撤去しようとするが、拒否されること。
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err == nil {
		t.Fatal("expected sync to refuse a destination outside every deployment root")
	}
	if _, err := os.Stat(filepath.Join(victim, "notes.md")); err != nil {
		t.Fatalf("a directory outside the deployment roots was touched: %v", err)
	}
}

// TestPruneKeepsArchivePinnedByLock は、lock がダイジェストで固定している
// 書庫のキャッシュを消さないことを確かめる。
//
// 消してしまうと、上流の URL が変わっていた場合にその版は二度と復元できず、
// lock の存在意義そのものが失われる。
func TestPruneKeepsArchivePinnedByLock(t *testing.T) {
	f := newFixture(t)
	const u = "https://example.com/toolkit.tar.gz"
	const dgst = "sha256:" +
		"1111111111111111111111111111111111111111111111111111111111111111"
	live := f.mkCache(t, store.ArchiveKey(u, dgst), "cached")

	a := f.open(t)
	a.lock.Put(lockfile.Entry{
		Name: "toolkit", Type: manifest.TypeSkill, Source: "url+" + u, Digest: dgst,
	})
	if err := a.persist(); err != nil {
		t.Fatal(err)
	}

	if _, err := f.open(t).Prune(context.Background(), PruneOptions{Apply: true, Store: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatal("an archive cache pinned by the lock was removed; that version is now unrecoverable")
	}
}

// TestSourceIDRedactsCredentials は、取得元 URL の資格情報が lock と表示に
// 残らないことを確かめる。lock はコミットされる前提のファイル。
func TestSourceIDRedactsCredentials(t *testing.T) {
	const secret = "ghp_SUPERSECRETTOKEN123"
	p := manifest.Package{
		Name: "private", Type: manifest.TypeSkill,
		Git: "https://x-access-token:" + secret + "@github.com/acme/skills",
	}
	if got := p.SourceID(); strings.Contains(got, secret) {
		t.Fatalf("SourceID() = %q, it must not carry the token", got)
	}
}

// TestManifestRejectsPlaintextGit は、経路上で差し替えられる取得元を
// 宣言できないことを確かめる。
func TestManifestRejectsPlaintextGit(t *testing.T) {
	f := newFixture(t)
	for _, src := range []string{"http://example.com/repo.git", "git://example.com/repo.git"} {
		a := f.open(t)
		if err := a.man.Add(manifest.Package{
			Name: "a", Type: manifest.TypeSkill, Git: src,
			Scope: manifest.ScopeUser, Strategy: manifest.StrategyLink,
		}); err != nil {
			t.Fatal(err)
		}
		if err := a.man.Validate(); err == nil {
			t.Fatalf("expected %q to be rejected", src)
		}
	}
}

// TestLockRejectsNonCommitPin は、lock に浮動参照を書けないことを確かめる。
// ブランチ名を書かれると固定という前提が崩れる。
func TestLockRejectsNonCommitPin(t *testing.T) {
	f := newFixture(t)
	body := "version: 1\nresolved:\n    - name: a\n      type: skill\n" +
		"      source: git+https://example.com/r\n      commit: main\n"
	if err := os.WriteFile(f.cfg.LockPath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := lockfile.Load(f.cfg.LockPath()); err == nil {
		t.Fatal("expected a lock entry pinned to a branch name to be rejected")
	}
}

// TestImportReportsRestrictedPermissions は、本人しか読めなかったファイルを
// 取り込んだときに知らせることを確かめる。
//
// 複製先は git 管理下で、権限は 0644 に緩む。そのまま push すると
// 内容と保護を同時に失う。
func TestImportReportsRestrictedPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits do not apply on this platform")
	}
	f := newFixture(t)
	dir := f.addClaudeSkill(t, "creds", "# creds\n")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("API_KEY=x"), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := f.open(t).Import(context.Background(), ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	it := itemsByName(rep)["creds"]
	var warned bool
	for _, n := range it.Notes {
		if strings.Contains(n, "readable only by you") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("notes = %v, want the restricted file to be reported", it.Notes)
	}
}
