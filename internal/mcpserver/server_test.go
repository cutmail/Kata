package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cutmail/kata/internal/app"
)

// newSession はサーバーとクライアントをインメモリのトランスポートで直結し、
// ハンドシェイク済みの ClientSession を返す。実際の stdio は経由しない。
func newSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	server := NewServer("test")
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)

	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()

	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })

	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

// callTool はツールを呼ぶ。トランスポート／プロトコルレベルのエラー（ツールが
// 存在しない等）は即座に Fatal にする。ツール実行自体の失敗は IsError で表現される
// ため、ここでは失敗として扱わない — 呼び出し側が明示的に確認する。
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

// decodeStructured は CallToolResult.StructuredContent を T にデコードする。
func decodeStructured[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	var out T
	data, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal structured content: %v\nraw: %s", err, data)
	}
	return out
}

// textOf は結果に含まれるテキストコンテンツを連結して返す。IsError のときの
// メッセージを読むのに使う。
func textOf(res *mcp.CallToolResult) string {
	var s string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			s += tc.Text
		}
	}
	return s
}

// newFixture は隔離された KATA_HOME/CLAUDE_CONFIG_DIR の下に空のマニフェスト用
// ディレクトリを用意する。実際の $HOME/.kata や $HOME/.claude には一切触れない。
func newFixture(t *testing.T) (dir, claudeHome string) {
	t.Helper()
	dir = t.TempDir()
	claudeHome = t.TempDir()
	t.Setenv("KATA_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	return dir, claudeHome
}

func addLocalSkill(t *testing.T, dir, name string) {
	t.Helper()
	skillDir := filepath.Join(dir, "local", "skills", name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestResolveLocalSource は dir を基点にした解決が、サーバープロセス自身の cwd に
// 依存しないことを確かめる（一度実際に壊れたことがある: dir とプロセスの cwd が
// 異なる状況で './' 始まりの source が誤ってプロセスの cwd から探された）。
func TestResolveLocalSource(t *testing.T) {
	cases := []struct {
		name, dir, source, want string
	}{
		{"relative dot-slash joins with dir", "/tmp/proj", "./local/skills/demo", "/tmp/proj/local/skills/demo"},
		{"relative dot-dot joins with dir", "/tmp/proj/sub", "../local/skills/demo", "/tmp/proj/local/skills/demo"},
		{"bare dot joins with dir", "/tmp/proj", ".", "/tmp/proj"},
		{"already-absolute path is untouched", "/tmp/proj", "/elsewhere/demo", "/elsewhere/demo"},
		{"git shorthand is untouched", "/tmp/proj", "owner/repo", "owner/repo"},
		{"git url is untouched", "/tmp/proj", "https://github.com/owner/repo", "https://github.com/owner/repo"},
		{"archive url is untouched", "/tmp/proj", "https://example.com/x.tar.gz", "https://example.com/x.tar.gz"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveLocalSource(c.dir, c.source); got != c.want {
				t.Fatalf("resolveLocalSource(%q, %q) = %q, want %q", c.dir, c.source, got, c.want)
			}
		})
	}
}

func TestInitAddListStatus(t *testing.T) {
	dir, _ := newFixture(t)
	cs := newSession(t)

	initRes := callTool(t, cs, "kata_init", map[string]any{"dir": dir})
	if initRes.IsError {
		t.Fatalf("kata_init failed: %s", textOf(initRes))
	}
	initOut := decodeStructured[initOutput](t, initRes)
	if initOut.Path != filepath.Join(dir, "kata.yml") {
		t.Fatalf("path = %q, want %s", initOut.Path, filepath.Join(dir, "kata.yml"))
	}

	addLocalSkill(t, dir, "demo")
	addRes := callTool(t, cs, "kata_add", map[string]any{
		"dir": dir, "source": "./local/skills/demo", "no_sync": true,
	})
	if addRes.IsError {
		t.Fatalf("kata_add failed: %s", textOf(addRes))
	}
	addOut := decodeStructured[addOutput](t, addRes)
	if addOut.Package.Name != "demo" || addOut.Sync != nil {
		t.Fatalf("unexpected add output: %+v", addOut)
	}

	listRes := callTool(t, cs, "kata_list", map[string]any{"dir": dir})
	if listRes.IsError {
		t.Fatalf("kata_list failed: %s", textOf(listRes))
	}
	listOut := decodeStructured[listOutput](t, listRes)
	if len(listOut.Items) != 1 || listOut.Items[0].Name != "demo" || listOut.Items[0].Status != app.StatusMissing {
		t.Fatalf("unexpected list output: %+v", listOut.Items)
	}

	statusRes := callTool(t, cs, "kata_status", map[string]any{"dir": dir})
	// 「ズレている」は正常な結果であり、ツールの失敗ではない。
	if statusRes.IsError {
		t.Fatalf("kata_status should not be a tool error just because things are out of sync: %s", textOf(statusRes))
	}
	statusOut := decodeStructured[app.StatusSummary](t, statusRes)
	if statusOut.Total != 1 || statusOut.Counts[app.StatusMissing] != 1 {
		t.Fatalf("unexpected status output: %+v", statusOut)
	}
}

// TestDirDefaultsToWorkingDirectory は dir を省略したとき、サーバープロセス自身の
// カレントディレクトリが使われることを確かめる（CLI の openApp() と同じ既定）。
func TestDirDefaultsToWorkingDirectory(t *testing.T) {
	dir, claudeHome := newFixture(t)
	t.Chdir(dir)
	cs := newSession(t)

	if res := callTool(t, cs, "kata_init", map[string]any{}); res.IsError {
		t.Fatalf("kata_init failed: %s", textOf(res))
	}
	addLocalSkill(t, dir, "demo")
	if res := callTool(t, cs, "kata_add", map[string]any{"source": "./local/skills/demo"}); res.IsError {
		t.Fatalf("kata_add failed: %s", textOf(res))
	}

	if _, err := os.Lstat(filepath.Join(claudeHome, "skills", "demo")); err != nil {
		t.Fatalf("deployment was not created under the inferred cwd: %v", err)
	}
}

// TestManifestNotFoundIsToolError は kata.yml が無いディレクトリに対する呼び出しが、
// プロセスをクラッシュさせたりプロトコルエラーにしたりせず、分かりやすい
// IsError 付きの結果として返ることを確かめる。
func TestManifestNotFoundIsToolError(t *testing.T) {
	newFixture(t)
	empty := t.TempDir()
	cs := newSession(t)

	res := callTool(t, cs, "kata_list", map[string]any{"dir": empty})
	if !res.IsError {
		t.Fatalf("expected IsError, got success: %+v", res.StructuredContent)
	}
	if got := textOf(res); got == "" {
		t.Fatal("expected a human-readable error message")
	}
}

// TestDoctorWorksWithoutManifest は doctor が他のツールと違い、マニフェストが
// 無くても診断できることを確かめる（internal/app.DefaultConfigLoose の挙動）。
func TestDoctorWorksWithoutManifest(t *testing.T) {
	newFixture(t)
	empty := t.TempDir()
	cs := newSession(t)

	res := callTool(t, cs, "kata_doctor", map[string]any{"dir": empty})
	if res.IsError {
		t.Fatalf("kata_doctor should work without a manifest: %s", textOf(res))
	}
	rep := decodeStructured[app.DoctorReport](t, res)
	if len(rep.Checks) == 0 {
		t.Fatal("expected at least one check")
	}
}

// TestRemoveUnlinksAndLeavesForeignFilesAlone は remove がツール経由でも実際に
// 配置を撤去すること、そして kata が置いていない隣接ファイルには触れないという
// 不変条件（CLAUDE.md の「守るべき不変条件 1」）を確かめる。
func TestRemoveUnlinksAndLeavesForeignFilesAlone(t *testing.T) {
	dir, claudeHome := newFixture(t)
	cs := newSession(t)

	if res := callTool(t, cs, "kata_init", map[string]any{"dir": dir}); res.IsError {
		t.Fatalf("kata_init failed: %s", textOf(res))
	}
	addLocalSkill(t, dir, "demo")
	if res := callTool(t, cs, "kata_add", map[string]any{"dir": dir, "source": "./local/skills/demo"}); res.IsError {
		t.Fatalf("kata_add failed: %s", textOf(res))
	}

	// kata を経由しない隣接ファイル。remove の巻き添えにならないことを確認する対象。
	foreign := filepath.Join(claudeHome, "skills", "foreign-untouched")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "SKILL.md"), []byte("# foreign\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(filepath.Join(claudeHome, "skills", "demo")); err != nil {
		t.Fatalf("precondition failed, deployment missing: %v", err)
	}

	res := callTool(t, cs, "kata_remove", map[string]any{"dir": dir, "name": "demo"})
	if res.IsError {
		t.Fatalf("kata_remove failed: %s", textOf(res))
	}
	out := decodeStructured[app.RemoveResult](t, res)
	if !out.Unlinked {
		t.Fatalf("unexpected result: %+v", out)
	}

	if _, err := os.Lstat(filepath.Join(claudeHome, "skills", "demo")); !os.IsNotExist(err) {
		t.Fatalf("deployment should be gone, stat err = %v", err)
	}
	if _, err := os.Lstat(foreign); err != nil {
		t.Fatalf("foreign entry should be untouched: %v", err)
	}
}

// TestPruneApplyRemovesOrphanedStoreEntry は prune がツール経由でも apply=true の
// ときだけ実際にキャッシュを削除することを、ネットワークに触れずに確かめる。
// 参照されていない store エントリを直接でっち上げることで再現する。
func TestPruneApplyRemovesOrphanedStoreEntry(t *testing.T) {
	dir, _ := newFixture(t)
	cs := newSession(t)

	if res := callTool(t, cs, "kata_init", map[string]any{"dir": dir}); res.IsError {
		t.Fatalf("kata_init failed: %s", textOf(res))
	}

	kataHome := os.Getenv("KATA_HOME")
	orphan := filepath.Join(kataHome, "store", "repos", "orphan-key")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// apply を付けない呼び出しは何も消さない。
	previewRes := callTool(t, cs, "kata_prune", map[string]any{"dir": dir, "store": true})
	if previewRes.IsError {
		t.Fatalf("kata_prune preview failed: %s", textOf(previewRes))
	}
	preview := decodeStructured[app.PruneReport](t, previewRes)
	if preview.Applied {
		t.Fatal("preview call should not report Applied")
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("preview must not remove anything: %v", err)
	}

	applyRes := callTool(t, cs, "kata_prune", map[string]any{"dir": dir, "store": true, "apply": true})
	if applyRes.IsError {
		t.Fatalf("kata_prune apply failed: %s", textOf(applyRes))
	}
	out := decodeStructured[app.PruneReport](t, applyRes)
	if !out.Applied || len(out.Items) != 1 || out.Items[0].Path != orphan {
		t.Fatalf("unexpected prune output: %+v", out)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphaned store entry should be gone, stat err = %v", err)
	}
}
