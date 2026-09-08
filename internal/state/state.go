// Package state persists the GitHub-issue-to-Todoist-task join table and the
// cached Todoist project name lookup.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// State is the on-disk contents of state.json.
type State struct {
	// SyncToken is reserved for a future incremental /sync integration.
	SyncToken     string           `json:"sync_token"`
	ProjectsCache ProjectsCache    `json:"projects_cache"`
	Issues        map[string]Issue `json:"issues"`

	path string
}

// ProjectsCache memoises the Todoist project name to id mapping so that a name
// configured in config.toml does not cost an API round trip on every run.
type ProjectsCache struct {
	FetchedAt time.Time         `json:"fetched_at"`
	ByName    map[string]string `json:"by_name"`
}

// Issue records one tracked GitHub issue and the Todoist task it produced.
type Issue struct {
	TodoistID string    `json:"todoist_id"`
	AddedAt   time.Time `json:"added_at"`
	// Tombstoned marks an issue that must never be re-added: the task was
	// completed or deleted in Todoist, or the issue was closed on GitHub.
	Tombstoned bool `json:"tombstoned"`
	// ClosedAt is set when the tombstone came from the GitHub issue closing.
	ClosedAt *time.Time `json:"closed_at"`
	// ReviveCount is how many times this issue has been revived. It feeds the
	// create-command uuid: without it a revived issue would reuse a uuid
	// Todoist has already executed and no new task would ever be created.
	ReviveCount int `json:"revive_count,omitempty"`
}

// DefaultPath returns ~/.local/state/gh-todoist/state.json, honouring
// XDG_STATE_HOME.
func DefaultPath() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "gh-todoist", "state.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "gh-todoist", "state.json"), nil
}

// Load reads state.json. A missing file yields an empty, usable State.
func Load(path string) (*State, error) {
	s := &State{path: path, Issues: map[string]Issue{}}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path, err)
	}
	s.path = path
	if s.Issues == nil {
		s.Issues = map[string]Issue{}
	}
	return s, nil
}

// Path reports where this State was loaded from and where Save writes.
func (s *State) Path() string { return s.path }

// Save writes the state atomically: a temp file in the destination directory
// followed by a rename, so a crash mid-write can never truncate the real file.
func (s *State) Save() error {
	if s.path == "" {
		return errors.New("save state: no path set")
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp state file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod temp state file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("rename temp state file into place: %w", err)
	}
	return nil
}

// Get returns the record for an issue URL.
func (s *State) Get(issueURL string) (Issue, bool) {
	i, ok := s.Issues[issueURL]
	return i, ok
}

// Track records a newly created task for an issue, clearing any tombstone and
// preserving the revive generation.
func (s *State) Track(issueURL, todoistID string, now time.Time) {
	if s.Issues == nil {
		s.Issues = map[string]Issue{}
	}
	prev := s.Issues[issueURL]
	s.Issues[issueURL] = Issue{
		TodoistID:   todoistID,
		AddedAt:     now,
		ReviveCount: prev.ReviveCount,
	}
}

// Generation returns the revive generation to use when building a create
// command for this issue.
func (s *State) Generation(issueURL string) int {
	return s.Issues[issueURL].ReviveCount
}

// TombstoneCompleted marks an issue whose Todoist task was completed or deleted
// on the Todoist side. The task id is kept so the record stays diagnosable.
func (s *State) TombstoneCompleted(issueURL string) {
	i, ok := s.Issues[issueURL]
	if !ok {
		return
	}
	i.Tombstoned = true
	s.Issues[issueURL] = i
}

// TombstoneClosed marks an issue that was closed on GitHub and whose task the
// tool has completed in Todoist.
func (s *State) TombstoneClosed(issueURL string, closedAt time.Time) {
	i, ok := s.Issues[issueURL]
	if !ok {
		return
	}
	i.Tombstoned = true
	t := closedAt
	i.ClosedAt = &t
	s.Issues[issueURL] = i
}

// Revive clears a tombstone so the issue can be pushed again, bumping the
// generation so the new create command carries an unused idempotency key.
func (s *State) Revive(issueURL string) {
	i, ok := s.Issues[issueURL]
	if !ok {
		return
	}
	i.Tombstoned = false
	i.ClosedAt = nil
	i.ReviveCount++
	s.Issues[issueURL] = i
}

// CacheProjects replaces the cached name to id map.
func (s *State) CacheProjects(byName map[string]string, now time.Time) {
	s.ProjectsCache = ProjectsCache{FetchedAt: now, ByName: byName}
}

// CachedProjectID looks up a project id by exact name from the cache.
func (s *State) CachedProjectID(name string) (string, bool) {
	if s.ProjectsCache.ByName == nil {
		return "", false
	}
	id, ok := s.ProjectsCache.ByName[name]
	return id, ok
}

// Counts summarises the tracked set for the status command.
func (s *State) Counts() (tracked, tombstoned int) {
	for _, i := range s.Issues {
		tracked++
		if i.Tombstoned {
			tombstoned++
		}
	}
	return tracked, tombstoned
}
