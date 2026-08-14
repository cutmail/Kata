package target

import (
	"path/filepath"
	"testing"

	"github.com/cutmail/kata/internal/manifest"
)

const (
	testHome    = "/home/u/.claude"
	testProject = "/work/repo/.claude"
)

func newTestResolver() *ClaudeCode { return NewClaudeCode(testHome, testProject) }

func TestClaudeCodeResolve(t *testing.T) {
	r := newTestResolver()

	cases := []struct {
		scope string
		typ   string
		name  string
		want  string
	}{
		{manifest.ScopeUser, manifest.TypeSkill, "pdf", filepath.Join(testHome, "skills", "pdf")},
		{manifest.ScopeUser, manifest.TypeCommand, "pr", filepath.Join(testHome, "commands", "pr.md")},
		{manifest.ScopeUser, manifest.TypeAgent, "reviewer", filepath.Join(testHome, "agents", "reviewer.md")},
		{manifest.ScopeProject, manifest.TypeSkill, "pdf", filepath.Join(testProject, "skills", "pdf")},
		{manifest.ScopeProject, manifest.TypeCommand, "pr", filepath.Join(testProject, "commands", "pr.md")},
	}
	for _, tc := range cases {
		p := manifest.Package{Name: tc.name, Type: tc.typ, Scope: tc.scope}
		got, err := r.Resolve(p)
		if err != nil || got != tc.want {
			t.Fatalf("%s/%s dest = %q, %v; want %q", tc.scope, tc.typ, got, err, tc.want)
		}
	}
}

func TestClaudeCodeRejectsUnsupported(t *testing.T) {
	r := newTestResolver()
	if _, err := r.Resolve(manifest.Package{Name: "x", Type: "prompt", Scope: manifest.ScopeUser}); err == nil {
		t.Fatal("expected an error for an unsupported type")
	}
	if _, err := r.Resolve(manifest.Package{Name: "x", Type: manifest.TypeSkill, Scope: "machine"}); err == nil {
		t.Fatal("expected an error for an unsupported scope")
	}
}

// TestClaudeCodeRejectsProjectWithoutProjectHome は、プロジェクトの位置が分からないまま
// project スコープを解決しないことを確かめる。ここで空文字を通すと、
// ファイルシステムのルート直下へ配置しようとしてしまう。
func TestClaudeCodeRejectsProjectWithoutProjectHome(t *testing.T) {
	r := NewClaudeCode(testHome, "")
	p := manifest.Package{Name: "x", Type: manifest.TypeSkill, Scope: manifest.ScopeProject}
	if _, err := r.Resolve(p); err == nil {
		t.Fatal("expected an error when the project directory is unknown")
	}
	if _, _, ok := r.Location(manifest.ScopeProject, manifest.TypeSkill); ok {
		t.Fatal("expected Location to report the project scope as unavailable")
	}
}

func TestClaudeCodeCapabilities(t *testing.T) {
	r := newTestResolver()
	for _, typ := range []string{manifest.TypeSkill, manifest.TypeCommand, manifest.TypeAgent} {
		if !r.Supports(typ) {
			t.Fatalf("Supports(%q) = false, want true", typ)
		}
	}
	if r.Supports("prompt") {
		t.Fatal("Supports(\"prompt\") = true, want false")
	}
	// skill だけがディレクトリで、command と agent は単一ファイル。
	if !r.ExpectDir(manifest.TypeSkill) {
		t.Fatal("ExpectDir(skill) = false, want true")
	}
	for _, typ := range []string{manifest.TypeCommand, manifest.TypeAgent} {
		if r.ExpectDir(typ) {
			t.Fatalf("ExpectDir(%q) = true, want false", typ)
		}
	}
	if r.Name() != "claude-code" {
		t.Fatalf("name = %q", r.Name())
	}
}

// TestClaudeCodeLocationMatchesResolve は、走査規則（Location）と配置規則（Resolve）が
// 同じ結果になることを確かめる。ここがずれると import が既存の配置を見落とす。
func TestClaudeCodeLocationMatchesResolve(t *testing.T) {
	r := newTestResolver()

	for _, scope := range []string{manifest.ScopeUser, manifest.ScopeProject} {
		for _, typ := range r.Types() {
			dir, ext, ok := r.Location(scope, typ)
			if !ok {
				t.Fatalf("Location(%q, %q) reported unsupported", scope, typ)
			}
			p := manifest.Package{Name: "x", Type: typ, Scope: scope}
			want, err := r.Resolve(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := filepath.Join(dir, "x"+ext); got != want {
				t.Fatalf("%s/%s: Location gives %q but Resolve gives %q", scope, typ, got, want)
			}
		}
	}
}

func TestClaudeCodeTypes(t *testing.T) {
	r := newTestResolver()
	got := r.Types()
	if len(got) != 3 {
		t.Fatalf("types = %v, want 3 entries", got)
	}
	// 呼び出し側の書き換えが内部の表に漏れないこと。
	got[0] = "tampered"
	if r.Types()[0] == "tampered" {
		t.Fatal("Types must return a copy")
	}
}

func TestClaudeCodeLocationRejectsUnsupported(t *testing.T) {
	r := newTestResolver()
	if _, _, ok := r.Location(manifest.ScopeUser, "prompt"); ok {
		t.Fatal("expected an unsupported type to be reported")
	}
	if _, _, ok := r.Location("machine", manifest.TypeSkill); ok {
		t.Fatal("expected an unsupported scope to be reported")
	}
}
