//go:build unix

package engine

import (
	"os/exec"
	"syscall"
)

// isolate runs the engine in its own process group so killGroup can stop
// everything it started (dev servers, watchers) once the review ends.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd) }
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
