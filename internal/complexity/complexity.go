// Package complexity sizes a pull request from its diff so Otto can pick how hard to think about
// it (effort, model) and whether to split it between several reviewers. It is a deterministic
// heuristic: free, predictable, and it explains itself.
package complexity

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/OvictorVieira/otto.code.reviewer/internal/diff"
	"github.com/OvictorVieira/otto.code.reviewer/internal/engine"
)

// Thresholds of the three sizes. Light must stay under every light limit; heavy needs only one
// heavy signal. Lines are counted on code, not tests, so adding tests never makes a PR "bigger".
const (
	LightFiles = 5
	LightLines = 200
	HeavyFiles = 25
	HeavyLines = 800
	// HeavyModules is how many separate areas (top two directories) make a change hard to hold in one head.
	HeavyModules = 8
)

// DefaultSensitive are path words that mark risky areas. A repository adds its own in .otto.yml.
var segment = regexp.MustCompile(`[/._-]`)

var DefaultSensitive = []string{
	"auth", "authentication", "authorization", "authn", "authz", "login", "session", "sessions", "token", "oauth",
	"password", "passwords", "permission", "permissions", "rbac",
	"payment", "payments", "billing", "invoice", "invoices", "checkout", "wallet",
	"security", "crypto", "secret",
	"migration", "migrations",
	".github/workflows",
}

// contract marks changes to something other code or clients depend on.
var contract = regexp.MustCompile(`(?i)(\.proto$|\.graphql$|openapi|swagger|/migrations?/|\.sql$|schema\.|/contracts?/)`)

var testFile = regexp.MustCompile(`(?i)(_test\.|\.test\.|\.spec\.|^tests?/|/tests?/|/__tests__/|/testdata/|_test/)`)

// generated or vendored files do not take review effort.
var ignored = regexp.MustCompile(`(?i)(^|/)(vendor|node_modules|dist|build)/|\.lock$|-lock\.json$|\.sum$|\.min\.(js|css)$|\.(png|jpg|jpeg|gif|svg|ico|pdf|ttf|woff2?)$|(^|/)goldens?/`)

// Assessment is the size of a pull request and why.
type Assessment struct {
	Tier      engine.Tier
	Files     int
	Lines     int // added + removed lines of code (tests and generated files excluded)
	TestLines int
	NewFiles  int
	Modules   int
	Sensitive []string // sensitive paths touched
	Contract  []string // contract files touched
	Reasons   []string
}

// Summary is one line for the log and the review footer.
func (a Assessment) Summary() string {
	return fmt.Sprintf("%s (%s)", a.Tier, strings.Join(a.Reasons, "; "))
}

// Config is what a repository can add in .otto.yml at its root.
type Config struct {
	Sensitive []string `yaml:"sensitive"` // path globs or words for risky areas
}

// LoadConfig reads .otto.yml from a checkout; a missing file means no additions.
func LoadConfig(dir string) (Config, error) {
	var c Config
	b, err := os.ReadFile(filepath.Join(dir, ".otto.yml"))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	return c, yaml.Unmarshal(b, &c)
}

// Assess sizes a change. extra are the repository's own sensitive paths (globs or words).
func Assess(stats []diff.FileStat, extra []string) Assessment {
	var a Assessment
	modules := map[string]bool{}
	sensitive := map[string]bool{}
	for _, f := range stats {
		if ignored.MatchString(f.Path) {
			continue
		}
		a.Files++
		lines := f.Added + f.Removed
		if testFile.MatchString(f.Path) {
			a.TestLines += lines
		} else {
			a.Lines += lines
		}
		if f.New {
			a.NewFiles++
		}
		modules[module(f.Path)] = true
		if isSensitive(f.Path, extra) && !sensitive[f.Path] {
			sensitive[f.Path] = true
			a.Sensitive = append(a.Sensitive, f.Path)
		}
		if contract.MatchString("/" + f.Path) {
			a.Contract = append(a.Contract, f.Path)
		}
	}
	a.Modules = len(modules)
	sort.Strings(a.Sensitive)

	a.Reasons = append(a.Reasons, fmt.Sprintf("%d files, %d lines of code", a.Files, a.Lines))
	var heavy []string
	if a.Files > HeavyFiles {
		heavy = append(heavy, fmt.Sprintf("more than %d files", HeavyFiles))
	}
	if a.Lines > HeavyLines {
		heavy = append(heavy, fmt.Sprintf("more than %d lines", HeavyLines))
	}
	if a.Modules > HeavyModules {
		heavy = append(heavy, fmt.Sprintf("%d areas touched", a.Modules))
	}
	if len(a.Sensitive) > 0 {
		heavy = append(heavy, "touches "+list(a.Sensitive))
	}
	if len(a.Contract) > 0 {
		heavy = append(heavy, "changes a contract: "+list(a.Contract))
	}
	switch {
	case len(heavy) > 0:
		a.Tier = engine.Heavy
		a.Reasons = append(a.Reasons, heavy...)
	case a.Files <= LightFiles && a.Lines <= LightLines:
		a.Tier = engine.Light
	default:
		a.Tier = engine.Medium
	}
	if a.TestLines > 0 {
		a.Reasons = append(a.Reasons, fmt.Sprintf("%d lines of tests", a.TestLines))
	}
	return a
}

func module(p string) string {
	parts := strings.Split(path.Dir(p), "/")
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, "/")
}

func isSensitive(p string, extra []string) bool {
	lower := strings.ToLower(p)
	for _, w := range append(append([]string{}, DefaultSensitive...), extra...) {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" {
			continue
		}
		if strings.ContainsAny(w, "*?") {
			if globMatch(w, lower) {
				return true
			}
			continue
		}
		if strings.Contains(w, "/") {
			if strings.HasPrefix(lower, strings.Trim(w, "/")+"/") || strings.Contains(lower, "/"+strings.Trim(w, "/")+"/") {
				return true
			}
			continue
		}
		for _, seg := range segment.Split(lower, -1) {
			if seg == w { // whole words only: "tokens.dart" (design tokens) is not "token"
				return true
			}
		}
	}
	return false
}

// globMatch supports * (one segment part) and ** (any number of segments).
func globMatch(glob, p string) bool {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; {
		case strings.HasPrefix(glob[i:], "**/"):
			b.WriteString("(.*/)?")
			i += 2
		case strings.HasPrefix(glob[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(p)
}

func list(paths []string) string {
	if len(paths) <= 3 {
		return strings.Join(paths, ", ")
	}
	return strings.Join(paths[:3], ", ") + fmt.Sprintf(" and %d more", len(paths)-3)
}
