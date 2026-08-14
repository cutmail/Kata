package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAppliesDefaultsAndResolvesSources(t *testing.T) {
	path := write(t, `
version: 1
sources:
  upstream:
    git: https://example.com/repo
    ref: main
packages:
  - name: pdf
    type: skill
    from: upstream
    path: skills/pdf
`)
	m, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	p := m.Packages[0]
	if p.Git != "https://example.com/repo" || p.Ref != "main" {
		t.Fatalf("source not resolved: %+v", p)
	}
	if p.Scope != ScopeUser || p.Strategy != StrategyLink {
		t.Fatalf("defaults not applied: %+v", p)
	}
	if m.Dir() != filepath.Dir(path) {
		t.Fatalf("dir = %q", m.Dir())
	}
}

func TestPackageRefOverridesSourceRef(t *testing.T) {
	path := write(t, `
version: 1
sources:
  upstream:
    git: https://example.com/repo
    ref: main
packages:
  - name: pdf
    type: skill
    from: upstream
    ref: v1.0.0
`)
	m, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := m.Packages[0].Ref; got != "v1.0.0" {
		t.Fatalf("ref = %q, want v1.0.0", got)
	}
}

func TestLoadRejectsInvalidManifests(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"duplicate name", `
version: 1
packages:
  - {name: a, type: skill, local: ./x}
  - {name: a, type: command, local: ./y.md}
`, "duplicate"},
		{"path escape", `
version: 1
packages:
  - {name: a, type: skill, git: https://e.com/r, path: ../../etc}
`, "escape"},
		{"local escape", `
version: 1
packages:
  - {name: a, type: skill, local: ../outside}
`, "escape"},
		{"absolute local", `
version: 1
packages:
  - {name: a, type: skill, local: /etc/passwd}
`, "relative"},
		{"both git and local", `
version: 1
packages:
  - {name: a, type: skill, git: https://e.com/r, local: ./x}
`, "mutually exclusive"},
		{"no source", `
version: 1
packages:
  - {name: a, type: skill}
`, "required"},
		{"unknown type", `
version: 1
packages:
  - {name: a, type: prompt, local: ./x}
`, "unsupported type"},
		{"invalid name", `
version: 1
packages:
  - {name: Bad Name, type: skill, local: ./x}
`, "name must match"},
		{"unknown source ref", `
version: 1
packages:
  - {name: a, type: skill, from: nope}
`, "unknown source"},
		{"unsupported version", `
version: 99
packages: []
`, "unsupported manifest version"},
		{"unknown field", `
version: 1
packages:
  - {name: a, type: skill, local: ./x, bogus: 1}
`, "field bogus"},
		{"path with local", `
version: 1
packages:
  - {name: a, type: skill, local: ./x, path: sub}
`, "cannot be combined"},
		{"git and url", `
version: 1
packages:
  - {name: a, type: skill, git: https://e.com/r, url: https://e.com/a.tar.gz}
`, "mutually exclusive"},
		{"plain http url", `
version: 1
packages:
  - {name: a, type: skill, url: http://example.com/a.tar.gz}
`, "must use https"},
		{"url with ref", `
version: 1
packages:
  - {name: a, type: skill, url: https://e.com/a.tar.gz, ref: main}
`, "meaningless"},
		{"bad sha256", `
version: 1
packages:
  - {name: a, type: skill, url: https://e.com/a.tar.gz, sha256: nope}
`, "64 lowercase hex"},
		{"sha256 without url", `
version: 1
packages:
  - {name: a, type: skill, local: ./x, sha256: 0000000000000000000000000000000000000000000000000000000000000000}
`, "only applies to a url"},
		{"unknown scope", `
version: 1
packages:
  - {name: a, type: skill, local: ./x, scope: machine}
`, "unsupported scope"},
		{"unknown strategy", `
version: 1
packages:
  - {name: a, type: skill, local: ./x, strategy: hardlink}
`, "unsupported strategy"},
		{"bad profile name", `
version: 1
packages:
  - {name: a, type: skill, local: ./x, profiles: ["Work Machine"]}
`, "profile"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(write(t, tc.body))
			if err == nil {
				t.Fatalf("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	m := New(dir)
	if err := m.Add(Package{
		Name: "a", Type: TypeSkill, Local: "./local/skills/a",
		Scope: ScopeUser, Strategy: StrategyLink,
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Packages) != 1 || got.Packages[0].Name != "a" {
		t.Fatalf("round trip lost data: %+v", got.Packages)
	}
}

func TestAddRejectsDuplicates(t *testing.T) {
	m := New(t.TempDir())
	p := Package{Name: "a", Type: TypeSkill, Local: "./x", Scope: ScopeUser, Strategy: StrategyLink}
	if err := m.Add(p); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(p); err == nil {
		t.Fatal("expected a duplicate error")
	}
}

func TestRemove(t *testing.T) {
	m := New(t.TempDir())
	_ = m.Add(Package{Name: "a", Type: TypeSkill, Local: "./x"})
	if !m.Remove("a") {
		t.Fatal("expected removal to succeed")
	}
	if m.Remove("a") {
		t.Fatal("expected second removal to report false")
	}
}
