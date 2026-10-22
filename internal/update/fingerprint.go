package update

import (
	"fmt"
	"os"
	"path/filepath"
)

// Fingerprint identifies the binary file this process was started from, so a long-running
// watcher can tell that it was replaced on disk by an update. It is empty when the file
// cannot be read, for example for a moment while it is being replaced.
func Fingerprint() string {
	path, err := os.Executable()
	if err != nil {
		return ""
	}
	return FingerprintOf(path)
}

// FingerprintOf is Fingerprint for a given path.
func FingerprintOf(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", info.ModTime().UnixNano(), info.Size())
}
