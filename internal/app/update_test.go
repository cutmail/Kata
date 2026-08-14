package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cutmail/kata/internal/lockfile"
	"github.com/cutmail/kata/internal/manifest"
)

// mkUpstream は t.TempDir に git リポジトリを作り、パスと最初のコミットを返す。
//
// ネットワークを使わずに update の本質（ref を解決し直して lock を進める）を
// 検証できるようにするため、上流をローカルに用意する。
func mkUpstream(t *testing.T, base, body string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		// go-git の file トランスポートは git-upload-pack を外部実行する。
		t.Skip("git is not available in this environment")
	}
	// 手元のリポジトリを指す取得元はマニフェストのディレクトリ配下に限られる。
	dir := filepath.Join(base, "upstream")
	skill := filepath.Join(dir, "skills", "pdf")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=kata", "GIT_AUTHOR_EMAIL=kata@example.com",
			"GIT_COMMITTER_NAME=kata", "GIT_COMMITTER_EMAIL=kata@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q", "-b", "main")
	run("add", "-A")
	run("commit", "-q", "-m", "first")
	head := run("rev-parse", "HEAD")
	return dir, head[:40]
}

// commitTo は上流に 1 コミット足し、新しい HEAD を返す。
func commitTo(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "skills", "pdf", "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=kata", "GIT_AUTHOR_EMAIL=kata@example.com",
			"GIT_COMMITTER_NAME=kata", "GIT_COMMITTER_EMAIL=kata@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "second")
	return run("rev-parse", "HEAD")[:40]
}

// TestUpdateMovesLockForward は、可変 ref を解決し直して lock が進むことと、
// 配置も新しい内容に追随することを確かめる。
func TestUpdateMovesLockForward(t *testing.T) {
	f := newFixture(t)
	upstream, first := mkUpstream(t, f.repo, "v1\n")
	f.declare(t, manifest.Package{
		Name: "pdf", Type: manifest.TypeSkill,
		Git: upstream, Ref: "main", Path: "skills/pdf",
	})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	lk, err := lockfile.Load(f.cfg.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := lk.Get("pdf"); e.Commit != first {
		t.Fatalf("locked commit = %q, want %q", e.Commit, first)
	}

	// 上流が進む。sync だけでは追随しない（lock が正）。
	second := commitTo(t, upstream, "v2\n")
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	lk, _ = lockfile.Load(f.cfg.LockPath())
	if e, _ := lk.Get("pdf"); e.Commit != first {
		t.Fatalf("sync moved the lock on its own: %q", e.Commit)
	}

	// update で初めて進む。
	rep, err := f.open(t).Update(context.Background(), UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Counts()[ActionUpdate]; got != 1 {
		t.Fatalf("updated = %d, want 1 (changes: %+v)", got, rep.Changes)
	}
	lk, _ = lockfile.Load(f.cfg.LockPath())
	if e, _ := lk.Get("pdf"); e.Commit != second {
		t.Fatalf("locked commit = %q, want %q", e.Commit, second)
	}
	// 配置も新しい内容を指していること。
	body, err := os.ReadFile(filepath.Join(f.claude, "skills", "pdf", "SKILL.md"))
	if err != nil || string(body) != "v2\n" {
		t.Fatalf("deployed content = %q, %v; want v2", body, err)
	}
}

// TestUpdateDryRunDoesNotWriteLock は、dry-run が lock を書かないことを確かめる。
func TestUpdateDryRunDoesNotWriteLock(t *testing.T) {
	f := newFixture(t)
	upstream, _ := mkUpstream(t, f.repo, "v1\n")
	f.declare(t, manifest.Package{
		Name: "pdf", Type: manifest.TypeSkill,
		Git: upstream, Ref: "main", Path: "skills/pdf",
	})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.cfg.LockPath())
	if err != nil {
		t.Fatal(err)
	}

	commitTo(t, upstream, "v2\n")
	rep, err := f.open(t).Update(context.Background(), UpdateOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := rep.Counts()[ActionUpdate]; got != 1 {
		t.Fatalf("planned updates = %d, want 1", got)
	}
	after, err := os.ReadFile(f.cfg.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a dry run must not write the lock file")
	}
}

// TestUpdateSkipsLocalPackages は、固定すべきコミットを持たない local を
// 対象外にし、その理由を伝えることを確かめる。
func TestUpdateSkipsLocalPackages(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.declare(t, manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"})
	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.cfg.LockPath())
	if err != nil {
		t.Fatal(err)
	}

	rep, err := f.open(t).Update(context.Background(), UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Changes) != 1 || rep.Changes[0].Reason == "" {
		t.Fatalf("changes = %+v, want a skipped local package with a reason", rep.Changes)
	}
	after, err := os.ReadFile(f.cfg.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("updating a local-only manifest must not rewrite the lock")
	}
}

// TestUpdateRejectsUnknownName は、宣言されていない名前を弾くことを確かめる。
func TestUpdateRejectsUnknownName(t *testing.T) {
	f := newFixture(t)
	if _, err := f.open(t).Update(context.Background(), UpdateOptions{Names: []string{"nope"}}); err == nil {
		t.Fatal("expected an error for a package that is not declared")
	}
}
