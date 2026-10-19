// Package service installs `otto watch` as a macOS launchd agent, so it starts
// at login, restarts if it crashes, and catches up on pending requests.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
)

// Label identifies the launchd agent.
const Label = "dev.otto.reviewer"

var plist = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>{{ .Label }}</string>
  <key>ProgramArguments</key>
  <array><string>{{ .Exe }}</string><string>watch</string></array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key><string>{{ .Path }}</string>
    <key>HOME</key><string>{{ .Home }}</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>30</integer>
  <key>StandardOutPath</key><string>{{ .Log }}</string>
  <key>StandardErrorPath</key><string>{{ .Log }}</string>
</dict>
</plist>
`))

func paths() (plistPath, logPath string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"),
		filepath.Join(home, "Library", "Logs", "otto.log"), nil
}

func domain() string { return fmt.Sprintf("gui/%d", os.Getuid()) }

// Install writes the launchd agent for the current binary and starts it.
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
	plistPath, logPath, err := paths()
	if err != nil {
		return "", err
	}
	home, _ := os.UserHomeDir()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(plistPath)
	if err != nil {
		return "", err
	}
	err = plist.Execute(f, map[string]string{
		"Label": Label, "Exe": exe, "Path": os.Getenv("PATH"), "Home": home, "Log": logPath,
	})
	f.Close()
	if err != nil {
		return "", err
	}
	_ = exec.Command("launchctl", "bootout", domain(), plistPath).Run()
	if out, err := exec.Command("launchctl", "bootstrap", domain(), plistPath).CombinedOutput(); err != nil {
		return "", fmt.Errorf("launchctl bootstrap: %w: %s", err, out)
	}
	return logPath, nil
}

// Uninstall stops and removes the launchd agent.
func Uninstall() error {
	plistPath, _, err := paths()
	if err != nil {
		return err
	}
	_ = exec.Command("launchctl", "bootout", domain(), plistPath).Run()
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Status reports whether the agent is loaded, and where it logs.
func Status() (bool, string) {
	_, logPath, _ := paths()
	err := exec.Command("launchctl", "print", domain()+"/"+Label).Run()
	return err == nil, logPath
}
