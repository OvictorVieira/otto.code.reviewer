// Package config stores Otto's settings in ~/.config/otto/config.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
)

// Repos selects which repositories the watcher polls.
type Repos struct {
	All      bool     `yaml:"all"`
	Selected []string `yaml:"selected"`
}

// GitHubApp is Otto's own GitHub identity (otto[bot]). When set, reviews asked
// for with the trigger comment are posted by the app instead of by the user.
type GitHubApp struct {
	ID      int64  `yaml:"id"`
	KeyPath string `yaml:"private_key"`
}

// Config is the user's Otto setup.
type Config struct {
	Trigger        string          `yaml:"trigger"`
	Language       string          `yaml:"language"`
	AllowedAuthors []string        `yaml:"allowed_authors"`
	Repos          Repos           `yaml:"repos"`
	App            GitHubApp       `yaml:"github_app,omitempty"`
	Strategy       engine.Strategy `yaml:"strategy"`
	Engines        []engine.Spec   `yaml:"engines"`
	Verify         bool            `yaml:"verify"`
	MinConfidence  float64         `yaml:"min_confidence"`
	PollInterval   time.Duration   `yaml:"poll_interval"`
	Lookback       time.Duration   `yaml:"lookback"`
	Timeout        time.Duration   `yaml:"timeout"`
}

// Default returns a working configuration before setup customizes it.
func Default() Config {
	return Config{
		Trigger:       "/otto review",
		Language:      "English",
		Strategy:      engine.RoundRobin,
		Verify:        true,
		MinConfidence: 0.3,
		PollInterval:  60 * time.Second,
		Lookback:      24 * time.Hour,
		Timeout:       30 * time.Minute,
	}
}

// Dir is where Otto keeps its config and state.
func Dir() (string, error) {
	if d := os.Getenv("OTTO_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "otto"), nil
}

// Path is the config file location.
func Path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.yaml"), nil
}

// ErrNotConfigured means `otto setup` has not been run yet.
var ErrNotConfigured = errors.New("otto is not configured yet: run `otto setup`")

// Load reads the config file and fills missing values with defaults.
func Load() (Config, error) {
	p, err := Path()
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, ErrNotConfigured
	}
	if err != nil {
		return Config{}, err
	}
	c := Default()
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", p, err)
	}
	if len(c.Engines) == 0 {
		return Config{}, fmt.Errorf("%s: no engines configured: run `otto setup`", p)
	}
	return c, nil
}

// Save writes the config file.
func Save(c Config) (string, error) {
	p, err := Path()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return "", err
	}
	return p, os.WriteFile(p, b, 0o600)
}
