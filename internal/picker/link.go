package picker

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// LinksEnabled says whether the terminal is trusted to show clickable hyperlinks (OSC 8). Terminals
// that do not know the sequence ignore it and show the text, but a dumb terminal would print it.
var LinksEnabled = os.Getenv("TERM") != "dumb" && os.Getenv("NO_HYPERLINKS") == ""

// OpenURL opens a link in the default browser. It is a variable so tests can replace it.
var OpenURL = func(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Run()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Run()
	default:
		return exec.Command("xdg-open", url).Run()
	}
}

// CopyText puts text on the clipboard. It is a variable so tests can replace it.
var CopyText = func(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "windows":
		cmd = exec.Command("clip")
	default:
		if _, err := exec.LookPath("wl-copy"); err == nil {
			cmd = exec.Command("wl-copy")
		} else {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		}
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// Link renders text as a clickable terminal hyperlink to url, or as plain text where links are off.
func Link(url, text string) string {
	if !LinksEnabled {
		return text
	}
	return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", url, text)
}
