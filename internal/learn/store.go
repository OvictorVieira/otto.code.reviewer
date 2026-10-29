package learn

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Memory is everything Otto remembers about one repository.
type Memory struct {
	Records     map[string]Record `json:"records"` // key: "<pr>:<comment id>"
	Lessons     []Lesson          `json:"lessons"`
	LastSweep   time.Time         `json:"last_sweep"`
	DistilledAt time.Time         `json:"distilled_at"`
}

// Store keeps one JSON file per repository under dir.
type Store struct{ Dir string }

func (s Store) path(repo string) string {
	return filepath.Join(s.Dir, strings.ReplaceAll(repo, "/", "__")+".json")
}

// Load reads the memory of repo; a repository Otto has not learned from yet is empty.
func (s Store) Load(repo string) (Memory, error) {
	m := Memory{Records: map[string]Record{}}
	b, err := os.ReadFile(s.path(repo))
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return Memory{Records: map[string]Record{}}, err
	}
	if m.Records == nil {
		m.Records = map[string]Record{}
	}
	return m, nil
}

// Save writes the memory atomically.
func (s Store) Save(repo string, m Memory) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(repo) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(repo))
}

func key(r Record) string { return strconv.Itoa(r.PR) + ":" + strconv.FormatInt(r.ID, 10) }

// Merge records fresh feedback. A record whose outcome changed is learned again,
// so a thumbs down that later becomes "Fixed in abc" updates what Otto believes.
// It returns how many records are new or changed.
func (m *Memory) Merge(recs []Record) int {
	changed := 0
	for _, r := range recs {
		old, ok := m.Records[key(r)]
		if ok && old.Outcome == r.Outcome && equalNotes(old.Notes, r.Notes) {
			continue
		}
		r.Learned = false
		m.Records[key(r)] = r
		if r.Outcome.Settled() {
			changed++
		}
	}
	return changed
}

func equalNotes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Unlearned returns settled records not yet folded into the lessons, oldest first.
func (m Memory) Unlearned() []Record {
	var out []Record
	for _, r := range m.Records {
		if r.Outcome.Settled() && !r.Learned {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seen.Before(out[j].Seen) })
	return out
}

// ForPR returns the findings Otto already made on one pull request.
func (m Memory) ForPR(pr int) []Record {
	var out []Record
	for _, r := range m.Records {
		if r.PR == pr {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// MarkLearned flags records as folded into the lessons.
func (m *Memory) MarkLearned(recs []Record) {
	for _, r := range recs {
		if cur, ok := m.Records[key(r)]; ok {
			cur.Learned = true
			m.Records[key(r)] = cur
		}
	}
}
