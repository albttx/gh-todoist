package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempState(t *testing.T) *State {
	t.Helper()
	s, err := Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return s
}

func TestLoadMissingFileIsUsable(t *testing.T) {
	t.Parallel()
	s := tempState(t)
	if s.Issues == nil {
		t.Fatal("Issues map must be initialised so callers can write immediately")
	}
	tracked, tombstoned := s.Counts()
	if tracked != 0 || tombstoned != 0 {
		t.Errorf("Counts() = %d, %d, want 0, 0", tracked, tombstoned)
	}
}

func TestSaveIsAtomicAndRoundTrips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "state.json")

	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	s.Track("https://github.com/o/r/issues/1", "task123", now)
	s.CacheProjects(map[string]string{"Chores 🧹": "proj123"}, now)
	if err := s.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// No temp file may survive a successful save.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory contains %v, want only state.json", names)
	}

	back, err := Load(path)
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	rec, ok := back.Get("https://github.com/o/r/issues/1")
	if !ok {
		t.Fatal("tracked issue did not survive the round trip")
	}
	if rec.TodoistID != "task123" || !rec.AddedAt.Equal(now) {
		t.Errorf("record = %+v", rec)
	}
	if id, ok := back.CachedProjectID("Chores 🧹"); !ok || id != "proj123" {
		t.Errorf("project cache did not survive: %q %v", id, ok)
	}
}

func TestSavedShapeMatchesTheDocumentedSchema(t *testing.T) {
	t.Parallel()
	s := tempState(t)
	s.Track("https://github.com/o/r/issues/1", "task123", time.Now().UTC())
	if err := s.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"sync_token", "projects_cache", "issues"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("state.json is missing the %q key", key)
		}
	}

	var issues map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw["issues"], &issues); err != nil {
		t.Fatalf("unmarshal issues: %v", err)
	}
	rec := issues["https://github.com/o/r/issues/1"]
	for _, key := range []string{"todoist_id", "added_at", "tombstoned", "closed_at"} {
		if _, ok := rec[key]; !ok {
			t.Errorf("issue record is missing the %q key", key)
		}
	}
}

func TestTombstoneTransitions(t *testing.T) {
	t.Parallel()
	url := "https://github.com/o/r/issues/1"
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)

	t.Run("completed in todoist", func(t *testing.T) {
		t.Parallel()
		s := tempState(t)
		s.Track(url, "t1", now)
		s.TombstoneCompleted(url)

		rec, _ := s.Get(url)
		if !rec.Tombstoned {
			t.Error("expected the record to be tombstoned")
		}
		if rec.ClosedAt != nil {
			t.Error("a Todoist-side completion must not set closed_at, which means closed on GitHub")
		}
		if rec.TodoistID != "t1" {
			t.Error("the task id is kept so the record stays diagnosable")
		}
	})

	t.Run("closed on github", func(t *testing.T) {
		t.Parallel()
		s := tempState(t)
		s.Track(url, "t1", now)
		closedAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		s.TombstoneClosed(url, closedAt)

		rec, _ := s.Get(url)
		if !rec.Tombstoned {
			t.Error("expected the record to be tombstoned")
		}
		if rec.ClosedAt == nil || !rec.ClosedAt.Equal(closedAt) {
			t.Errorf("ClosedAt = %v, want %v", rec.ClosedAt, closedAt)
		}
	})

	t.Run("revive clears the tombstone and bumps the generation", func(t *testing.T) {
		t.Parallel()
		s := tempState(t)
		s.Track(url, "t1", now)
		s.TombstoneClosed(url, now)
		s.Revive(url)

		rec, _ := s.Get(url)
		if rec.Tombstoned {
			t.Error("revive must clear the tombstone")
		}
		if rec.ClosedAt != nil {
			t.Error("revive must clear closed_at")
		}
		if rec.ReviveCount != 1 {
			t.Errorf("ReviveCount = %d, want 1: the create uuid depends on it", rec.ReviveCount)
		}
	})

	t.Run("re-tracking preserves the generation", func(t *testing.T) {
		t.Parallel()
		s := tempState(t)
		s.Track(url, "t1", now)
		s.TombstoneCompleted(url)
		s.Revive(url)
		s.Track(url, "t2", now)

		rec, _ := s.Get(url)
		if rec.ReviveCount != 1 {
			t.Errorf("ReviveCount = %d, want the generation to survive Track", rec.ReviveCount)
		}
		if rec.Tombstoned {
			t.Error("Track must clear any tombstone")
		}
		if rec.TodoistID != "t2" {
			t.Errorf("TodoistID = %q, want the new task id", rec.TodoistID)
		}
	})

	t.Run("transitions on an unknown issue are no-ops", func(t *testing.T) {
		t.Parallel()
		s := tempState(t)
		s.TombstoneCompleted("https://github.com/o/r/issues/99")
		s.TombstoneClosed("https://github.com/o/r/issues/99", now)
		s.Revive("https://github.com/o/r/issues/99")
		if len(s.Issues) != 0 {
			t.Errorf("mutating an unknown issue created a record: %+v", s.Issues)
		}
	})
}

func TestGeneration(t *testing.T) {
	t.Parallel()
	s := tempState(t)
	url := "https://github.com/o/r/issues/1"
	if got := s.Generation(url); got != 0 {
		t.Errorf("Generation of an unknown issue = %d, want 0", got)
	}
	s.Track(url, "t1", time.Now())
	s.TombstoneCompleted(url)
	s.Revive(url)
	s.TombstoneCompleted(url)
	s.Revive(url)
	if got := s.Generation(url); got != 2 {
		t.Errorf("Generation after two revives = %d, want 2", got)
	}
}

func TestCounts(t *testing.T) {
	t.Parallel()
	s := tempState(t)
	now := time.Now().UTC()
	s.Track("https://github.com/o/r/issues/1", "t1", now)
	s.Track("https://github.com/o/r/issues/2", "t2", now)
	s.Track("https://github.com/o/r/issues/3", "t3", now)
	s.TombstoneCompleted("https://github.com/o/r/issues/2")

	tracked, tombstoned := s.Counts()
	if tracked != 3 || tombstoned != 1 {
		t.Errorf("Counts() = %d, %d, want 3, 1", tracked, tombstoned)
	}
}

func TestDefaultPathHonoursXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/custom/state")
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() error = %v", err)
	}
	if got != "/custom/state/gh-todoist/state.json" {
		t.Errorf("DefaultPath() = %q", got)
	}
}

func TestLoadRejectsCorruptState(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Failing loudly beats silently starting from an empty join table, which
	// would re-push every issue the user already curated.
	if _, err := Load(path); err == nil {
		t.Fatal("expected corrupt state to be an error, not a silent reset")
	}
}
