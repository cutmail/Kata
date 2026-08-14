package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/cutmail/kata/internal/app"
)

// TestStatusIsItsOwnCommand は status が list のエイリアスに戻っていないことを確かめる。
// エイリアスに戻ると status 固有の終了コードが失われ、CI での利用が黙って壊れる。
func TestStatusIsItsOwnCommand(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"status"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name() != "status" {
		t.Fatalf("status resolved to %q, want status", cmd.Name())
	}
}

// TestListKeepsShortAlias は ls が list を指し続けることを確かめる。
func TestListKeepsShortAlias(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"ls"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name() != "list" {
		t.Fatalf("ls resolved to %q, want list", cmd.Name())
	}
}

// ---- --json 出力のテスト ----
//
// --json は os.Stdout に直接書くため（cobra の OutOrStdout は経由しない）、
// captureStdout で標準出力そのものを一時的に差し替えて捕捉する。

// captureStdout は fn の実行中だけ os.Stdout をパイプに差し替え、書き込まれた内容を返す。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = orig
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// runCLI は kata の cobra コマンドツリーをプロセス内で実行し、標準出力とエラーを返す。
func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var runErr error
	out := captureStdout(t, func() {
		cmd := newRootCmd()
		cmd.SetArgs(args)
		runErr = cmd.Execute()
	})
	return out, runErr
}

// newFixture は隔離された KATA_HOME/CLAUDE_CONFIG_DIR の下に kata.yml を 1 つ用意し、
// 作業ディレクトリと配置先(ClaudeHome)を返す。実際の $HOME/.kata や $HOME/.claude には
// 一切触れない。
func newFixture(t *testing.T) (dir, claudeHome string) {
	t.Helper()
	dir = t.TempDir()
	claudeHome = t.TempDir()
	t.Setenv("KATA_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	t.Chdir(dir)
	if _, err := runCLI(t, "init"); err != nil {
		t.Fatalf("init: %v", err)
	}
	return dir, claudeHome
}

// addLocalSkill は dir 配下にローカル skill を 1 つ作り、そのパスを返す。
func addLocalSkill(t *testing.T, dir, name string) string {
	t.Helper()
	skillDir := filepath.Join(dir, "local", "skills", name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return skillDir
}

func TestInitJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KATA_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Chdir(dir)

	out, err := runCLI(t, "init", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res initResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if res.Path == "" {
		t.Fatal("path is empty")
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Fatalf("created path does not exist: %v", err)
	}
}

func TestListJSON(t *testing.T) {
	dir, _ := newFixture(t)
	addLocalSkill(t, dir, "demo")
	if _, err := runCLI(t, "add", "./local/skills/demo", "--no-sync"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res itemsResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if len(res.Items) != 1 || res.Items[0].Name != "demo" {
		t.Fatalf("unexpected items: %+v", res.Items)
	}
	if res.Items[0].Status != app.StatusMissing {
		t.Fatalf("status = %q, want %q (not synced yet)", res.Items[0].Status, app.StatusMissing)
	}
}

// TestAddJSON は --json でも add が実際に同期まで行う(出力形式が違うだけ)ことを確かめる。
func TestAddJSON(t *testing.T) {
	dir, claudeHome := newFixture(t)
	addLocalSkill(t, dir, "demo")

	out, err := runCLI(t, "add", "./local/skills/demo", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res addResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if res.Package.Name != "demo" {
		t.Fatalf("package name = %q, want demo", res.Package.Name)
	}
	if res.Sync == nil || len(res.Sync.Changes) != 1 || res.Sync.Changes[0].Action != app.ActionCreate {
		t.Fatalf("unexpected sync report: %+v", res.Sync)
	}
	if _, err := os.Lstat(filepath.Join(claudeHome, "skills", "demo")); err != nil {
		t.Fatalf("deployment was not created: %v", err)
	}
}

func TestSyncJSON(t *testing.T) {
	dir, claudeHome := newFixture(t)
	addLocalSkill(t, dir, "demo")
	if _, err := runCLI(t, "add", "./local/skills/demo", "--no-sync"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, "sync", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rep app.SyncReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if len(rep.Changes) != 1 || rep.Changes[0].Action != app.ActionCreate {
		t.Fatalf("unexpected changes: %+v", rep.Changes)
	}
	if _, err := os.Lstat(filepath.Join(claudeHome, "skills", "demo")); err != nil {
		t.Fatalf("deployment was not created: %v", err)
	}
}

// TestImportJSON は kata を経由せず配置先に直接置かれた実体を取り込めることを確かめる。
func TestImportJSON(t *testing.T) {
	dir, claudeHome := newFixture(t)
	foreign := filepath.Join(claudeHome, "skills", "foreign")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "SKILL.md"), []byte("# foreign\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, "import", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rep app.ImportReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if len(rep.Items) != 1 || rep.Items[0].Action != app.ImportImported || rep.Items[0].Name != "foreign" {
		t.Fatalf("unexpected items: %+v", rep.Items)
	}
	if _, err := os.Stat(filepath.Join(dir, "local", "skills", "foreign")); err != nil {
		t.Fatalf("not copied into local/: %v", err)
	}
	// import は既定では配置先に一切触れない(CLAUDE.md の設計上の注意)。
	if _, err := os.Lstat(foreign); err != nil {
		t.Fatalf("original should be untouched: %v", err)
	}
}

// TestUpdateJSON は local パッケージ(ピンを持たない)が unchanged として報告されることを、
// ネットワークに触れずに確かめる。
func TestUpdateJSON(t *testing.T) {
	dir, _ := newFixture(t)
	addLocalSkill(t, dir, "demo")
	if _, err := runCLI(t, "add", "./local/skills/demo", "--no-sync"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, "update", "--json", "--no-sync")
	if err != nil {
		t.Fatal(err)
	}
	var rep app.UpdateReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if len(rep.Changes) != 1 || rep.Changes[0].Action != app.ActionUnchanged {
		t.Fatalf("unexpected changes: %+v", rep.Changes)
	}
	if rep.Sync != nil {
		t.Fatalf("--no-sync should leave sync nil, got %+v", rep.Sync)
	}
}

func TestPruneJSON(t *testing.T) {
	newFixture(t)

	out, err := runCLI(t, "prune", "--store", "--staging", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rep app.PruneReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if len(rep.Items) != 0 {
		t.Fatalf("unexpected items on a fresh manifest: %+v", rep.Items)
	}
}

func TestRemoveJSON(t *testing.T) {
	dir, claudeHome := newFixture(t)
	addLocalSkill(t, dir, "demo")
	if _, err := runCLI(t, "add", "./local/skills/demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(claudeHome, "skills", "demo")); err != nil {
		t.Fatalf("precondition failed, deployment missing: %v", err)
	}

	out, err := runCLI(t, "remove", "demo", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res app.RemoveResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if !res.Unlinked {
		t.Fatalf("unexpected result: %+v", res)
	}
	if _, err := os.Lstat(filepath.Join(claudeHome, "skills", "demo")); !os.IsNotExist(err) {
		t.Fatalf("deployment should be gone, stat err = %v", err)
	}
}

// TestStatusJSON は status --json が、テキスト版と同じく「ズレ」を非 0 終了で示しつつも
// JSON 自体は正しく出力し続けることを確かめる(exitError はメッセージが空の特別な終了コード)。
func TestStatusJSON(t *testing.T) {
	dir, _ := newFixture(t)
	addLocalSkill(t, dir, "demo")
	if _, err := runCLI(t, "add", "./local/skills/demo", "--no-sync"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, "status", "--json")
	if err != nil && err.Error() != "" {
		t.Fatalf("unexpected error: %v", err)
	}
	var sum app.StatusSummary
	if err := json.Unmarshal([]byte(out), &sum); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if sum.Total != 1 || sum.Counts[app.StatusMissing] != 1 {
		t.Fatalf("unexpected summary: %+v", sum)
	}
}

func TestDoctorJSON(t *testing.T) {
	newFixture(t)

	out, err := runCLI(t, "doctor", "--json")
	if err != nil && err.Error() != "" {
		t.Fatalf("unexpected error: %v", err)
	}
	var rep app.DoctorReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out)
	}
	if len(rep.Checks) == 0 {
		t.Fatal("expected at least one check")
	}
}
