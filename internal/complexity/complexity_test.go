package complexity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OvictorVieira/otto.code.reviewer/internal/diff"
	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
)

func files(n, lines int, dir string) []diff.FileStat {
	var out []diff.FileStat
	for i := 0; i < n; i++ {
		out = append(out, diff.FileStat{Path: fmt.Sprintf("%s/f%d.go", dir, i), Added: lines})
	}
	return out
}

func TestASmallChangeIsLight(t *testing.T) {
	a := Assess(files(3, 40, "internal/stock"), nil)
	if a.Tier != engine.Light {
		t.Fatalf("got %s", a.Summary())
	}
}

func TestAnEverydayChangeIsMedium(t *testing.T) {
	if a := Assess(files(10, 30, "internal/stock"), nil); a.Tier != engine.Medium {
		t.Fatalf("got %s", a.Summary())
	}
	if a := Assess(files(2, 150, "internal/stock"), nil); a.Tier != engine.Medium {
		t.Fatalf("300 lines must not be light: %s", a.Summary())
	}
}

func TestSizeAloneMakesAChangeHeavy(t *testing.T) {
	if a := Assess(files(26, 5, "internal/stock"), nil); a.Tier != engine.Heavy || !strings.Contains(a.Summary(), "more than 25 files") {
		t.Fatalf("got %s", a.Summary())
	}
	if a := Assess(files(4, 220, "internal/stock"), nil); a.Tier != engine.Heavy || !strings.Contains(a.Summary(), "more than 800 lines") {
		t.Fatalf("got %s", a.Summary())
	}
}

func TestOneSmallChangeToARiskyAreaIsHeavyAndSaysWhy(t *testing.T) {
	a := Assess([]diff.FileStat{{Path: "internal/auth/session.go", Added: 3}}, nil)
	if a.Tier != engine.Heavy || !strings.Contains(a.Summary(), "touches internal/auth/session.go") {
		t.Fatalf("got %s", a.Summary())
	}
	if a := Assess([]diff.FileStat{{Path: "db/migrations/0004_add_index.sql", Added: 2}}, nil); a.Tier != engine.Heavy {
		t.Fatalf("a migration is a contract change: %s", a.Summary())
	}
}

func TestWordsMatchWholePathPartsOnly(t *testing.T) {
	// SetTune's design tokens are not auth tokens.
	a := Assess([]diff.FileStat{{Path: "packages/settune_ui/lib/src/tokens.dart", Added: 10}}, nil)
	if a.Tier == engine.Heavy {
		t.Fatalf("tokens.dart must not be sensitive: %s", a.Summary())
	}
}

func TestTestsGeneratedFilesAndLockfilesDoNotInflateTheSize(t *testing.T) {
	stats := append(files(2, 50, "internal/stock"),
		diff.FileStat{Path: "internal/stock/stock_test.go", Added: 900},
		diff.FileStat{Path: "go.sum", Added: 400},
		diff.FileStat{Path: "apps/live/test/goldens/phone-stage.png", Added: 1},
	)
	a := Assess(stats, nil)
	if a.Tier != engine.Light || a.Lines != 100 || a.TestLines != 900 || a.Files != 3 {
		t.Fatalf("got %+v", a)
	}
}

func TestManyAreasMakeAChangeHeavy(t *testing.T) {
	var stats []diff.FileStat
	for i := 0; i < 9; i++ {
		stats = append(stats, diff.FileStat{Path: fmt.Sprintf("mod%d/sub/a.go", i), Added: 2})
	}
	if a := Assess(stats, nil); a.Tier != engine.Heavy {
		t.Fatalf("got %s", a.Summary())
	}
}

func TestARepositoryAddsItsOwnSensitivePathsInOttoYml(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".otto.yml"), []byte("sensitive:\n  - \"apps/live/**/audio/**\"\n  - engine\n"), 0o600)
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a := Assess([]diff.FileStat{{Path: "apps/live/lib/audio/mixer.dart", Added: 5}}, cfg.Sensitive); a.Tier != engine.Heavy {
		t.Fatalf("glob from .otto.yml ignored: %s", a.Summary())
	}
	if a := Assess([]diff.FileStat{{Path: "native/engine/render.swift", Added: 5}}, cfg.Sensitive); a.Tier != engine.Heavy {
		t.Fatalf("word from .otto.yml ignored: %s", a.Summary())
	}
	if none, err := LoadConfig(t.TempDir()); err != nil || len(none.Sensitive) != 0 {
		t.Fatal("a missing .otto.yml is no configuration, not an error")
	}
}
