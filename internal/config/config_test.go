package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
)

func TestLoadWithoutSetupAsksToRunSetup(t *testing.T) {
	t.Setenv("OTTO_HOME", t.TempDir())

	if _, err := Load(); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

func TestLoadFillsDefaultsForMissingFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OTTO_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("engines:\n  - name: claude\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := Load()

	if err != nil {
		t.Fatal(err)
	}
	if c.Trigger != "/otto review" || c.Strategy != engine.RoundRobin || !c.Verify || c.Timeout != 30*time.Minute {
		t.Errorf("defaults not applied: %+v", c)
	}
}

func TestLoadRejectsConfigWithoutEngines(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OTTO_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("trigger: /otto review\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err == nil {
		t.Fatal("a config without engines must be rejected")
	}
}

func TestSaveThenLoadKeepsEverySetting(t *testing.T) {
	t.Setenv("OTTO_HOME", t.TempDir())
	want := Default()
	want.Repos = Repos{Selected: []string{"o/a", "o/b"}}
	want.Engines = []engine.Spec{{Name: "codex", Model: "gpt-6-luna", Effort: "high"}, {Name: "cursor"}}
	want.Strategy = engine.Priority
	want.Verify = false
	want.PollInterval = 2 * time.Minute

	if _, err := Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := Load()

	if err != nil {
		t.Fatal(err)
	}
	if got.Strategy != want.Strategy || got.Verify || got.PollInterval != want.PollInterval ||
		len(got.Engines) != 2 || got.Engines[0] != want.Engines[0] || len(got.Repos.Selected) != 2 {
		t.Errorf("round trip lost data:\n got %+v\nwant %+v", got, want)
	}
}

func TestStampChangesWhenTheConfigIsRewrittenAndIsEmptyWhenUnreadableOrEmpty(t *testing.T) {
	t.Setenv("OTTO_HOME", t.TempDir())
	if got := Stamp(); got != "" {
		t.Fatalf("stamp without a config = %q, want empty", got)
	}

	c := Default()
	c.Engines = []engine.Spec{{Name: "codex", Model: "gpt-6-luna"}}
	if _, err := Save(c); err != nil {
		t.Fatal(err)
	}
	before := Stamp()
	if before == "" {
		t.Fatal("stamp of a saved config must not be empty")
	}
	if Stamp() != before {
		t.Error("stamp must be stable while the config does not change")
	}

	c.Engines = append(c.Engines, engine.Spec{Name: "opencode", Model: "opencode/beam-free"})
	if _, err := Save(c); err != nil {
		t.Fatal(err)
	}
	if Stamp() == before {
		t.Error("stamp must change after the config is saved again")
	}

	path, _ := Path()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Stamp(); got != "" {
		t.Errorf("stamp of a file being rewritten (empty) = %q, want empty so no restart reads a half-written config", got)
	}
}
