package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cutmail/kata/internal/manifest"
)

func TestStatusSummaryTracksDeployment(t *testing.T) {
	f := newFixture(t)
	f.addSkill(t, "a", "x")
	f.addSkill(t, "b", "y")
	f.declare(t,
		manifest.Package{Name: "a", Type: manifest.TypeSkill, Local: "./local/skills/a"},
		manifest.Package{Name: "b", Type: manifest.TypeSkill, Local: "./local/skills/b"},
	)

	// 配置前は 2 件ともズレている。
	sum, err := f.open(t).StatusSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sum.InSync() {
		t.Fatal("expected the summary to report a mismatch before syncing")
	}
	if got := sum.Counts[StatusMissing]; got != 2 {
		t.Fatalf("missing = %d, want 2 (counts: %v)", got, sum.Counts)
	}
	if len(sum.Drifted) != 2 || sum.Total != 2 {
		t.Fatalf("drifted = %d, total = %d, want 2 and 2", len(sum.Drifted), sum.Total)
	}

	if _, err := f.open(t).Sync(context.Background(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}

	sum, err = f.open(t).StatusSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !sum.InSync() {
		t.Fatalf("expected in sync after syncing, drifted = %+v", sum.Drifted)
	}
	if got := sum.Counts[StatusLinked]; got != 2 {
		t.Fatalf("linked = %d, want 2", got)
	}

	// リンクを壊すと、その 1 件だけがズレとして挙がる。
	dest := filepath.Join(f.claude, "skills", "b")
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.repo, "nowhere"), dest); err != nil {
		t.Fatal(err)
	}
	sum, err = f.open(t).StatusSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sum.InSync() {
		t.Fatal("expected a broken link to be reported")
	}
	if len(sum.Drifted) != 1 || sum.Drifted[0].Name != "b" {
		t.Fatalf("drifted = %+v, want only b", sum.Drifted)
	}
}

func TestStatusSummaryOnEmptyManifest(t *testing.T) {
	f := newFixture(t)
	sum, err := f.open(t).StatusSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// 宣言が無ければズレようがない。
	if !sum.InSync() || sum.Total != 0 {
		t.Fatalf("summary = %+v, want an empty in-sync summary", sum)
	}
}
