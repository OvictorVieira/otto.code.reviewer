package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestLoadIgnoresLegacyGitHubAppConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OTTO_HOME", dir)
	legacy := []byte("engines:\n  - name: claude\ngithub_app:\n  id: 123\n  private_key: /tmp/otto.pem\n")
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(); err != nil {
		t.Fatalf("legacy github_app field should be ignored: %v", err)
	}

	if _, err := Save(Default()); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "github_app") {
		t.Errorf("legacy github_app field persisted after save:\n%s", saved)
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
