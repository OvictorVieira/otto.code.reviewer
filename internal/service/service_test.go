package service

import (
	"strings"
	"testing"
)

func TestIsRunningRecognizesLaunchdStateAndPID(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{name: "running state", output: "state = running\n", want: true},
		{name: "running pid", output: "pid = 1234\n", want: true},
		{name: "waiting with no pid", output: "state = waiting\npid = 0\n", want: false},
		{name: "invalid pid", output: "state = waiting\npid = -\n", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRunning(tt.output); got != tt.want {
				t.Fatalf("isRunning(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func TestWatcherAndMonitorPlistsUseDistinctLifecycles(t *testing.T) {
	watcher, err := render(Label, []string{"/opt/otto", "watch"}, "/usr/bin", "/Users/test", "/tmp/otto.log", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(watcher), "<key>KeepAlive</key><true/>") ||
		!strings.Contains(string(watcher), "<string>watch</string>") {
		t.Fatalf("watcher plist must keep the watcher alive:\n%s", watcher)
	}

	monitor, err := render(monitorLabel, []string{"/opt/otto", "service", "monitor"}, "/usr/bin", "/Users/test", "/tmp/otto.log", false, monitorInterval)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(monitor), "<key>KeepAlive</key>") ||
		!strings.Contains(string(monitor), "<key>StartInterval</key><integer>300</integer>") ||
		!strings.Contains(string(monitor), "<string>monitor</string>") {
		t.Fatalf("monitor plist must run periodically without a persistent loop:\n%s", monitor)
	}
}
