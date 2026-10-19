package trigger

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// State is persisted between runs so a restart neither repeats nor loses work.
type State struct {
	Processed map[int64]time.Time  `json:"processed"`
	Cursors   map[string]time.Time `json:"cursors"`
	Rotation  int                  `json:"rotation"`
	path      string
}

// LoadState reads the state file, or starts empty when it does not exist.
func LoadState(path string) (*State, error) {
	s := &State{Processed: map[int64]time.Time{}, Cursors: map[string]time.Time{}, path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Processed == nil {
		s.Processed = map[int64]time.Time{}
	}
	if s.Cursors == nil {
		s.Cursors = map[string]time.Time{}
	}
	return s, nil
}

// Save writes the state atomically.
func (s *State) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Path is the state file location.
func (s *State) Path() string { return s.path }

// Done reports whether a comment was already handled.
func (s *State) Done(id int64) bool { _, ok := s.Processed[id]; return ok }

// MarkDone records a handled comment.
func (s *State) MarkDone(id int64, at time.Time) { s.Processed[id] = at }

// Cursor is the last poll time for repo, or fallback when never polled.
func (s *State) Cursor(repo string, fallback time.Time) time.Time {
	if t, ok := s.Cursors[repo]; ok {
		return t
	}
	return fallback
}

// SetCursor records the last poll time for repo.
func (s *State) SetCursor(repo string, at time.Time) { s.Cursors[repo] = at }

// Prune forgets handled comments older than maxAge.
func (s *State) Prune(now time.Time, maxAge time.Duration) {
	for id, at := range s.Processed {
		if now.Sub(at) > maxAge {
			delete(s.Processed, id)
		}
	}
}
