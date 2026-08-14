package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cutmail/kata/internal/manifest"
)

func TestNormalizeGitURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"anthropics/skills", "https://github.com/anthropics/skills"},
		{"github.com/anthropics/skills", "https://github.com/anthropics/skills"},
		{"https://github.com/anthropics/skills", "https://github.com/anthropics/skills"},
		{"git@github.com:anthropics/skills.git", "git@github.com:anthropics/skills.git"},
		{"https://gitlab.com/g/sub/repo", "https://gitlab.com/g/sub/repo"},
	}
	for _, tc := range cases {
		got, err := normalizeGitURL(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("normalizeGitURL(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "a/b/c/d"} {
		if _, err := normalizeGitURL(bad); err == nil {
			t.Errorf("normalizeGitURL(%q) should fail", bad)
		}
	}
}

func TestDefaultName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"skills/pdf", "pdf"},
		{"commands/PR.md", "pr"},
		{"https://github.com/anthropics/skills.git", "skills"},
		{"/abs/path/to/thing/", "thing"},
	}
	for _, tc := range cases {
		if got := defaultName(tc.in); got != tc.want {
			t.Errorf("defaultName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAddInfersLocalSkill(t *testing.T) {
	f := newFixture(t)
	dir := f.addSkill(t, "my-review", "x")

	p, err := f.open(t).Add(context.Background(), AddSpec{Source: dir})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "my-review" || p.Type != manifest.TypeSkill {
		t.Fatalf("package = %+v", p)
	}
	if p.Local != "./local/skills/my-review" {
		t.Fatalf("local = %q, want a manifest-relative path", p.Local)
	}

	// 書き戻したマニフェストが読み直せることまで確認する。
	m, err := manifest.Load(f.cfg.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Packages) != 1 {
		t.Fatalf("manifest has %d packages", len(m.Packages))
	}
}

func TestAddInfersLocalCommand(t *testing.T) {
	f := newFixture(t)
	p := f.addCommand(t, "pr")

	got, err := f.open(t).Add(context.Background(), AddSpec{Source: p})
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != manifest.TypeCommand || got.Name != "pr" {
		t.Fatalf("package = %+v", got)
	}
}

func TestAddGitSource(t *testing.T) {
	f := newFixture(t)
	p, err := f.open(t).Add(context.Background(), AddSpec{
		Source: "anthropics/skills", Path: "skills/pdf", Ref: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Git != "https://github.com/anthropics/skills" || p.Name != "pdf" || p.Type != manifest.TypeSkill {
		t.Fatalf("package = %+v", p)
	}
}

func TestAddGitSourceInfersCommandFromExtension(t *testing.T) {
	f := newFixture(t)
	p, err := f.open(t).Add(context.Background(), AddSpec{
		Source: "someone/dotfiles", Path: "commands/commit.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != manifest.TypeCommand || p.Name != "commit" {
		t.Fatalf("package = %+v", p)
	}
}

func TestAddRejectsLocalSourceOutsideManifest(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := f.open(t).Add(context.Background(), AddSpec{Source: outside}); err == nil {
		t.Fatal("expected an error for a source outside the manifest directory")
	}
}

func TestAddRejectsDuplicateName(t *testing.T) {
	f := newFixture(t)
	dir := f.addSkill(t, "dup", "x")
	if _, err := f.open(t).Add(context.Background(), AddSpec{Source: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.open(t).Add(context.Background(), AddSpec{Source: dir}); err == nil {
		t.Fatal("expected a duplicate name error")
	}
}

func TestAddRejectsPathWithLocalSource(t *testing.T) {
	f := newFixture(t)
	dir := f.addSkill(t, "a", "x")
	if _, err := f.open(t).Add(context.Background(), AddSpec{Source: dir, Path: "sub"}); err == nil {
		t.Fatal("expected --path to be rejected for local sources")
	}
}

// TestBuildPackageDetectsArchiveURL は、書庫の URL が git リポジトリと
// 誤認されないことを確かめる。誤認すると clone を試みて分かりにくく失敗する。
func TestBuildPackageDetectsArchiveURL(t *testing.T) {
	f := newFixture(t)
	a := f.open(t)

	cases := []struct {
		src    string
		wantIs bool
	}{
		{"https://example.com/toolkit-1.4.0.tar.gz", true},
		{"https://example.com/toolkit.tgz", true},
		{"https://example.com/toolkit.zip", true},
		{"https://example.com/toolkit.tar.gz?v=2", true},
		{"https://github.com/anthropics/skills", false},
		{"anthropics/skills", false},
	}
	for _, tc := range cases {
		p, err := a.buildPackage(AddSpec{Source: tc.src, Type: manifest.TypeSkill, Name: "x"})
		if err != nil {
			t.Fatalf("%s: %v", tc.src, err)
		}
		if got := p.IsURL(); got != tc.wantIs {
			t.Fatalf("%s: IsURL = %v, want %v", tc.src, got, tc.wantIs)
		}
	}
}

// TestBuildPackageHonoursURLFlag は、拡張子で判断がつかないときに
// --url が効くことを確かめる。
func TestBuildPackageHonoursURLFlag(t *testing.T) {
	f := newFixture(t)
	a := f.open(t)

	p, err := a.buildPackage(AddSpec{
		Source: "https://example.com/download?id=42", Type: manifest.TypeSkill,
		Name: "x", IsURL: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsURL() {
		t.Fatalf("package = %+v, want it treated as a url source", p)
	}
}
