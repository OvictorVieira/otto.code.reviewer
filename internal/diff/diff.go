// Package diff reads a unified diff and answers which new-side lines a
// review comment can be attached to.
package diff

import (
	"regexp"
	"strconv"
	"strings"
)

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// Diff maps each file path to its new-side lines and the hunk they belong to.
type Diff struct {
	order []string
	lines map[string]map[int]int
}

// Parse reads a unified diff such as the output of `gh pr diff`.
func Parse(text string) Diff {
	d := Diff{lines: map[string]map[int]int{}}
	var path string
	var next, hunk int
	inHunk := false

	for _, raw := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(raw, "diff --git "):
			path, inHunk = "", false
		case strings.HasPrefix(raw, "+++ "):
			path = strings.TrimPrefix(strings.TrimPrefix(raw, "+++ "), "b/")
			if path == "/dev/null" {
				path = ""
				continue
			}
			if _, ok := d.lines[path]; !ok {
				d.lines[path] = map[int]int{}
				d.order = append(d.order, path)
			}
		case strings.HasPrefix(raw, "@@"):
			m := hunkHeader.FindStringSubmatch(raw)
			if m == nil || path == "" {
				inHunk = false
				continue
			}
			next, _ = strconv.Atoi(m[1])
			hunk++
			inHunk = true
		case inHunk && path != "":
			switch {
			case strings.HasPrefix(raw, "+"), strings.HasPrefix(raw, " "):
				d.lines[path][next] = hunk
				next++
			case strings.HasPrefix(raw, "-"), strings.HasPrefix(raw, `\`):
				// removed lines and "\ No newline" markers have no new-side line
			default:
				inHunk = false
			}
		}
	}
	return d
}

// Commentable reports whether line exists on the new side of the diff for path.
func (d Diff) Commentable(path string, line int) bool {
	_, ok := d.lines[path][line]
	return ok
}

// SameHunk reports whether both lines are commentable and inside the same hunk,
// which GitHub requires for a multi-line comment.
func (d Diff) SameHunk(path string, start, end int) bool {
	a, okA := d.lines[path][start]
	b, okB := d.lines[path][end]
	return okA && okB && a == b
}

// Files lists changed files that still exist after the change, in diff order.
func (d Diff) Files() []string {
	return append([]string(nil), d.order...)
}

// FileStat is the size of one file's change.
type FileStat struct {
	Path    string
	Added   int
	Removed int
	New     bool // the file did not exist before
	Deleted bool // the file no longer exists
}

// Stats counts added and removed lines per file, including deleted files, in diff order.
func Stats(text string) []FileStat {
	var out []FileStat
	var cur *FileStat
	inHunk := false
	for _, raw := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(raw, "diff --git "):
			out = append(out, FileStat{})
			cur, inHunk = &out[len(out)-1], false
			if f := strings.Fields(raw); len(f) >= 4 {
				cur.Path = strings.TrimPrefix(f[3], "b/")
			}
		case cur == nil:
		case strings.HasPrefix(raw, "new file mode"):
			cur.New = true
		case strings.HasPrefix(raw, "deleted file mode"):
			cur.Deleted = true
		case strings.HasPrefix(raw, "@@"):
			inHunk = true
		case !inHunk:
		case strings.HasPrefix(raw, "+"):
			cur.Added++
		case strings.HasPrefix(raw, "-"):
			cur.Removed++
		}
	}
	return out
}
