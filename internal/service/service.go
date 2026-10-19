// Package service installs `otto watch` and a periodic launchd health check.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"
)

// Label identifies the launchd agent.
const Label = "dev.otto.reviewer"

const (
	monitorLabel    = Label + ".monitor"
	monitorInterval = 300
)

var plist = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{{ .Label }}</string>
  <key>ProgramArguments</key>
  <array>{{ range .Args }}<string>{{ . }}</string>{{ end }}</array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key><string>{{ .Path }}</string>
    <key>HOME</key><string>{{ .Home }}</string>
  </dict>
  <key>RunAtLoad</key><true/>
  {{ if .KeepAlive }}<key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>30</integer>{{ end }}
  {{ if .Interval }}<key>StartInterval</key><integer>{{ .Interval }}</integer>{{ end }}
  <key>StandardOutPath</key><string>{{ .Log }}</string>
  <key>StandardErrorPath</key><string>{{ .Log }}</string>
</dict>
</plist>
`))

func paths() (plistPath, monitorPlistPath, logPath string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", "", err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	return filepath.Join(dir, Label+".plist"),
		filepath.Join(dir, monitorLabel+".plist"),
		filepath.Join(home, "Library", "Logs", "otto.log"), nil
}

func domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

func render(label string, args []string, path, home, log string, keepAlive bool, interval int) ([]byte, error) {
	var out strings.Builder
	err := plist.Execute(&out, struct {
		Label     string
		Args      []string
		Path      string
		Home      string
		Log       string
		KeepAlive bool
		Interval  int
	}{label, args, path, home, log, keepAlive, interval})
	return []byte(out.String()), err
}

func writePlist(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

// Install writes the launchd agent for the current binary and starts it.
// A second agent checks every five minutes and restarts the watcher if needed.
// The current PATH is captured so launchd finds gh, git and the engine CLIs.
func Install() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("background service is only automated on macOS; run `otto watch` with your init system")
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", err
	}
	if strings.Contains(exe, "go-build") {
		return "", fmt.Errorf("run the installed binary (go install ./cmd/otto), not `go run`")
	}
	plistPath, monitorPlistPath, logPath, err := paths()
	if err != nil {
		return "", err
	}
	home, _ := os.UserHomeDir()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return "", err
	}
	path := os.Getenv("PATH")
	watcher, err := render(Label, []string{exe, "watch"}, path, home, logPath, true, 0)
	if err != nil {
		return "", err
	}
	monitor, err := render(monitorLabel, []string{exe, "service", "monitor"}, path, home, logPath, false, monitorInterval)
	if err != nil {
		return "", err
	}
	if err := writePlist(plistPath, watcher); err != nil {
		return "", err
	}
	if err := writePlist(monitorPlistPath, monitor); err != nil {
		return "", err
	}

	_ = exec.Command("launchctl", "bootout", domain(), plistPath).Run()
	_ = exec.Command("launchctl", "bootout", domain(), monitorPlistPath).Run()
	if out, err := exec.Command("launchctl", "bootstrap", domain(), plistPath).CombinedOutput(); err != nil {
		return "", fmt.Errorf("launchctl bootstrap: %w: %s", err, out)
	}
	if out, err := exec.Command("launchctl", "bootstrap", domain(), monitorPlistPath).CombinedOutput(); err != nil {
		return "", fmt.Errorf("launchctl bootstrap health check: %w: %s", err, out)
	}
	return logPath, nil
}

// Uninstall stops and removes the launchd agent.
func Uninstall() error {
	plistPath, monitorPlistPath, _, err := paths()
	if err != nil {
		return err
	}
	_ = exec.Command("launchctl", "bootout", domain(), plistPath).Run()
	_ = exec.Command("launchctl", "bootout", domain(), monitorPlistPath).Run()
	for _, path := range []string{plistPath, monitorPlistPath} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Ensure starts the watcher when it is not loaded or no longer running.
func Ensure() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("background service is only automated on macOS")
	}
	plistPath, _, _, err := paths()
	if err != nil {
		return err
	}
	target := domain() + "/" + Label
	out, err := exec.Command("launchctl", "print", target).CombinedOutput()
	if err != nil {
		out, err = exec.Command("launchctl", "bootstrap", domain(), plistPath).CombinedOutput()
		if err != nil {
			return fmt.Errorf("launchctl bootstrap watcher: %w: %s", err, out)
		}
		return nil
	}
	if !isRunning(string(out)) {
		out, err = exec.Command("launchctl", "kickstart", "-k", target).CombinedOutput()
		if err != nil {
			return fmt.Errorf("launchctl restart watcher: %w: %s", err, out)
		}
	}
	return nil
}

func isRunning(output string) bool {
	running := false
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "state":
			running = strings.TrimSpace(value) == "running"
		case "pid":
			pid, err := strconv.Atoi(strings.TrimSpace(value))
			running = running || err == nil && pid > 0
		}
	}
	return running
}

// Status reports whether the agent process is running, and where it logs.
func Status() (bool, string) {
	_, _, logPath, _ := paths()
	out, err := exec.Command("launchctl", "print", domain()+"/"+Label).Output()
	return err == nil && isRunning(string(out)), logPath
}
