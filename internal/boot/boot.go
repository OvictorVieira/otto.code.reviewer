// Package boot must be imported by main. Go initializes independent packages
// in import-path order, so this one runs before bubbletea, whose init asks the
// terminal for its background color. That question's late answer can arrive as
// keystrokes in the next screen. Deciding the color here means we never ask.
package boot

import (
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func init() { lipgloss.SetHasDarkBackground(dark(os.Getenv("COLORFGBG"))) }

// dark reads COLORFGBG ("fg;bg") when the terminal sets it, else assumes dark.
func dark(colorfgbg string) bool {
	parts := strings.Split(colorfgbg, ";")
	if bg, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
		return bg < 7 || bg == 8
	}
	return true
}
