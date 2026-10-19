//go:build unix

package update

import (
	"os"
	"syscall"
)

// Restart replaces the running process with the (new) binary, same arguments.
func Restart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
