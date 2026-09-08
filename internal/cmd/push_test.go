package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/albttx/gh-todoist/internal/config"
	"github.com/albttx/gh-todoist/internal/state"
	"github.com/albttx/gh-todoist/pkg/ghsrc"
	"github.com/albttx/gh-todoist/pkg/todoist"
)

// newTestApp builds an app backed by a temp state file and a fake Todoist.
func newTestApp(t *testing.T, handler http.HandlerFunc) (*app, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	st, err := state.Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	out := &bytes.Buffer{}
	return &app{
		cfg:    &config.Config{Todoist: config.Todoist{Label: "gh"}},
		st:     st,
		out:    out,
		errOut: &bytes.Buffer{},
		cwd:    t.TempDir(),
		td:     todoist.New(srv.URL, "tok", todoist.WithSleep(func(time.Duration) {})),
	}, out
}

func issue(n int, title string) ghsrc.Issue {
	return ghsrc.Issue{
		Ref:   ghsrc.Ref{Owner: "o", Repo: "r", Number: n},
		Title: title,
		State: "open",
	}
}

// decodeCommands pulls the commands array out of a form-encoded /sync body.
//
// Failures are reported with Errorf, not Fatalf: this runs on the test server's
// goroutine, and FailNow may only be called from the goroutine running the test.
func decodeCommands(t *testing.T, r *http.Request) []todoist.Command {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Errorf("parse form: %v", err)
		return nil
	}
	var cmds []todoist.Command
	if err := json.Unmarshal([]byte(r.PostForm.Get("commands")), &cmds); err != nil {
		t.Errorf("decode commands: %v", err)
		return nil
	}
	return cmds
}

// writeSyncResponse renders a /sync reply from a per-uuid status map and a
// temp_id mapping.
func writeSyncResponse(t *testing.T, w http.ResponseWriter, status map[string]any, mapping map[string]string) {
	t.Helper()
	err := json.NewEncoder(w).Encode(map[string]any{
		"sync_status": status, "temp_id_mapping": mapping,
	})
	if err != nil {
		t.Errorf("encode sync response: %v", err)
	}
}

// okHandler answers every command with success and a synthetic task id.
func okHandler(t *testing.T, seen *[]todoist.Command) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		cmds := decodeCommands(t, r)
		if seen != nil {
			*seen = append(*seen, cmds...)
		}
		status := map[string]any{}
		mapping := map[string]string{}
		for i, c := range cmds {
			status[c.UUID] = "ok"
			mapping[c.TempID] = fmt.Sprintf("task%d", i)
		}
		writeSyncResponse(t, w, status, mapping)
	}
}

func TestPushAddsUntrackedIssues(t *testing.T) {
	t.Parallel()
	var seen []todoist.Command
	a, _ := newTestApp(t, okHandler(t, &seen))

	issues := []ghsrc.Issue{issue(1, "first"), issue(2, "second")}
	results, err := a.push(context.Background(), issues, pushOptions{
		Project: resolvedProject{ID: "proj1", Display: "Code"},
	})
	if err != nil {
		t.Fatalf("push() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for i, r := range results {
		if r.Outcome != outcomeAdded {
			t.Errorf("results[%d].Outcome = %v, want added (%s)", i, r.Outcome, r.Detail)
		}
	}

	// Both issues must ride in a single batched request.
	if len(seen) != 2 {
		t.Errorf("sent %d commands, want 2 in one batch", len(seen))
	}
	for _, c := range seen {
		args, ok := c.Args.(map[string]any)
		if !ok {
			t.Fatalf("args = %#v", c.Args)
		}
		if args["project_id"] != "proj1" {
			t.Errorf("project_id = %v, want the resolved project", args["project_id"])
		}
	}

	if rec, ok := a.st.Get(issues[0].URL()); !ok || rec.TodoistID == "" {
		t.Errorf("issue 1 was not tracked with a task id: %+v", rec)
	}
}

func TestPushSkipsAlreadyTracked(t *testing.T) {
	t.Parallel()
	calls := 0
	a, _ := newTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		okHandler(t, nil)(w, r)
	})

	tracked := issue(1, "already there")
	a.st.Track(tracked.URL(), "existing", time.Now().UTC())

	results, err := a.push(context.Background(), []ghsrc.Issue{tracked}, pushOptions{})
	if err != nil {
		t.Fatalf("push() error = %v", err)
	}
	if results[0].Outcome != outcomeAlreadyTracked {
		t.Errorf("Outcome = %v, want alreadyTracked", results[0].Outcome)
	}
	if calls != 0 {
		t.Error("an already-tracked issue must not cost a request")
	}
	if rec, _ := a.st.Get(tracked.URL()); rec.TodoistID != "existing" {
		t.Errorf("the existing task id was overwritten: %q", rec.TodoistID)
	}
}

func TestPushRespectsTombstones(t *testing.T) {
	t.Parallel()
	calls := 0
	a, _ := newTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		okHandler(t, nil)(w, r)
	})

	dead := issue(1, "completed in todoist")
	a.st.Track(dead.URL(), "t1", time.Now().UTC())
	a.st.TombstoneCompleted(dead.URL())

	results, err := a.push(context.Background(), []ghsrc.Issue{dead}, pushOptions{})
	if err != nil {
		t.Fatalf("push() error = %v", err)
	}
	if results[0].Outcome != outcomeTombstoned {
		t.Errorf("Outcome = %v, want tombstoned", results[0].Outcome)
	}
	if !strings.Contains(results[0].Detail, "--revive") {
		t.Errorf("Detail = %q, want it to mention --revive", results[0].Detail)
	}
	if calls != 0 {
		t.Error("a tombstoned issue must not be pushed")
	}
}

func TestPushReviveUsesAFreshUUID(t *testing.T) {
	t.Parallel()
	var seen []todoist.Command
	a, _ := newTestApp(t, okHandler(t, &seen))

	dead := issue(1, "back from the dead")
	a.st.Track(dead.URL(), "t1", time.Now().UTC())
	a.st.TombstoneCompleted(dead.URL())

	results, err := a.push(context.Background(), []ghsrc.Issue{dead}, pushOptions{Revive: true})
	if err != nil {
		t.Fatalf("push() error = %v", err)
	}
	if results[0].Outcome != outcomeAdded {
		t.Errorf("Outcome = %v, want added (%s)", results[0].Outcome, results[0].Detail)
	}
	if len(seen) != 1 {
		t.Fatalf("sent %d commands, want 1", len(seen))
	}
	// Reusing the original uuid would make Todoist refuse the command, so the
	// revived task would never be created.
	if seen[0].UUID == todoist.CommandUUID(dead.URL()) {
		t.Error("revive reused the original uuid; Todoist would reject it as already executed")
	}
	rec, _ := a.st.Get(dead.URL())
	if rec.Tombstoned {
		t.Error("a successful revive must clear the tombstone")
	}
	if rec.ReviveCount != 1 {
		t.Errorf("ReviveCount = %d, want 1", rec.ReviveCount)
	}
}

// TestPushFailedReviveKeepsTheTombstone is the regression guard for a state
// corruption: clearing the tombstone before Todoist confirmed the create left
// the issue permanently wedged — reported as "already tracked" with no task
// behind it, and unreachable by --revive because that path needs the tombstone.
func TestPushFailedReviveKeepsTheTombstone(t *testing.T) {
	t.Parallel()
	a, _ := newTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		status := map[string]any{}
		for _, c := range decodeCommands(t, r) {
			status[c.UUID] = map[string]any{"error": "Project not found", "error_code": 404}
		}
		writeSyncResponse(t, w, status, map[string]string{})
	})

	dead := issue(1, "revive that fails")
	a.st.Track(dead.URL(), "t1", time.Now().UTC())
	a.st.TombstoneCompleted(dead.URL())

	results, err := a.push(context.Background(), []ghsrc.Issue{dead}, pushOptions{Revive: true})
	if err != nil {
		t.Fatalf("push() error = %v", err)
	}
	if results[0].Outcome != outcomeFailed {
		t.Fatalf("Outcome = %v, want failed", results[0].Outcome)
	}

	rec, _ := a.st.Get(dead.URL())
	if !rec.Tombstoned {
		t.Error("a failed revive must leave the tombstone in place, so --revive can be retried")
	}
	if rec.ReviveCount != 0 {
		t.Errorf("ReviveCount = %d, want 0: the generation must not advance on failure", rec.ReviveCount)
	}
}

func TestPushHandlesPartialFailure(t *testing.T) {
	t.Parallel()

	good := issue(1, "ok")
	bad := issue(2, "rejected")

	a, _ := newTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		status := map[string]any{}
		mapping := map[string]string{}
		for _, c := range decodeCommands(t, r) {
			if c.UUID == todoist.CommandUUID(bad.URL()) {
				status[c.UUID] = map[string]any{"error": "Project not found", "error_code": 404}
				continue
			}
			status[c.UUID] = "ok"
			mapping[c.TempID] = "realtask"
		}
		writeSyncResponse(t, w, status, mapping)
	})

	results, err := a.push(context.Background(), []ghsrc.Issue{good, bad}, pushOptions{})
	if err != nil {
		t.Fatalf("push() error = %v, want nil: a partial failure is reported per issue", err)
	}
	if results[0].Outcome != outcomeAdded {
		t.Errorf("good issue outcome = %v", results[0].Outcome)
	}
	if results[1].Outcome != outcomeFailed {
		t.Fatalf("bad issue outcome = %v, want failed", results[1].Outcome)
	}
	if !strings.Contains(results[1].Detail, "Project not found") {
		t.Errorf("Detail = %q, want the server's reason", results[1].Detail)
	}

	// The critical invariant: a failed command must not enter the join table,
	// or the tool would believe a task exists that never got created.
	if _, ok := a.st.Get(bad.URL()); ok {
		t.Error("a failed push was recorded in state")
	}
	if _, ok := a.st.Get(good.URL()); !ok {
		t.Error("the successful push was not recorded in state")
	}
}

// TestPushHandlesMissingTempIDMapping covers the already-executed uuid case:
// Todoist answers ok but returns no id, so the issue is tracked with an empty
// id for `sync` to recover from the label listing.
func TestPushHandlesMissingTempIDMapping(t *testing.T) {
	t.Parallel()
	a, _ := newTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		status := map[string]any{}
		for _, c := range decodeCommands(t, r) {
			status[c.UUID] = "ok"
		}
		writeSyncResponse(t, w, status, map[string]string{})
	})

	dup := issue(1, "pushed before, state was lost")
	results, err := a.push(context.Background(), []ghsrc.Issue{dup}, pushOptions{})
	if err != nil {
		t.Fatalf("push() error = %v", err)
	}
	if results[0].Outcome != outcomeAdded {
		t.Errorf("Outcome = %v, want added", results[0].Outcome)
	}
	rec, ok := a.st.Get(dup.URL())
	if !ok {
		t.Fatal("the issue must still be tracked")
	}
	if rec.TodoistID != "" {
		t.Errorf("TodoistID = %q, want empty pending recovery", rec.TodoistID)
	}
	if !strings.Contains(results[0].Detail, "recovered") {
		t.Errorf("Detail = %q, want it to explain the pending recovery", results[0].Detail)
	}
}

// TestPushDeduplicatesRepeatedIssues covers `gh todoist add 812 812`: both
// mentions derive the same command uuid, so without deduplication the second
// would overwrite the first in the pending map and be reported as a failure.
func TestPushDeduplicatesRepeatedIssues(t *testing.T) {
	t.Parallel()
	var seen []todoist.Command
	a, _ := newTestApp(t, okHandler(t, &seen))

	dup := issue(1, "named twice")
	results, err := a.push(context.Background(), []ghsrc.Issue{dup, dup}, pushOptions{})
	if err != nil {
		t.Fatalf("push() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1 after deduplication", len(results))
	}
	if results[0].Outcome != outcomeAdded {
		t.Errorf("Outcome = %v, want added (%s)", results[0].Outcome, results[0].Detail)
	}
	if len(seen) != 1 {
		t.Errorf("sent %d commands, want 1", len(seen))
	}
	if rec, _ := a.st.Get(dup.URL()); rec.TodoistID == "" {
		t.Error("the deduplicated issue was not tracked with a task id")
	}
}

func TestPushWithNoCommandsMakesNoRequest(t *testing.T) {
	t.Parallel()
	calls := 0
	a, _ := newTestApp(t, func(w http.ResponseWriter, r *http.Request) { calls++ })
	results, err := a.push(context.Background(), nil, pushOptions{})
	if err != nil {
		t.Fatalf("push() error = %v", err)
	}
	if len(results) != 0 || calls != 0 {
		t.Errorf("results = %v, calls = %d, want no work", results, calls)
	}
}

func TestPrintPushResults(t *testing.T) {
	t.Parallel()
	a, out := newTestApp(t, func(w http.ResponseWriter, r *http.Request) {})
	failures := a.printPushResults([]pushResult{
		{Issue: issue(1, "added one"), Outcome: outcomeAdded},
		{Issue: issue(2, "known"), Outcome: outcomeAlreadyTracked},
		{Issue: issue(3, "dead"), Outcome: outcomeTombstoned, Detail: "use --revive"},
		{Issue: issue(4, "broke"), Outcome: outcomeFailed, Detail: "boom"},
	})
	if failures != 1 {
		t.Errorf("failures = %d, want 1", failures)
	}
	text := out.String()
	for _, want := range []string{"✓ added", "• already tracked", "⊘ tombstoned", "use --revive", "✗ failed", "boom", "o/r#1"} {
		if !strings.Contains(text, want) {
			t.Errorf("output missing %q:\n%s", want, text)
		}
	}
	if lines := strings.Count(strings.TrimSpace(text), "\n") + 1; lines != 4 {
		t.Errorf("printed %d lines, want one per issue", lines)
	}
}

func TestScaffoldIsValidTOMLAndDocumentsPrecedence(t *testing.T) {
	t.Parallel()
	body := scaffold([]string{"Chores 🧹", "Work"})

	// Everything but the label line is commented out, so a fresh config must
	// parse and yield only the default label.
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("the generated config does not parse: %v\n%s", err, body)
	}
	if cfg.Label() != "gh" {
		t.Errorf("Label() = %q, want gh", cfg.Label())
	}
	if cfg.Token() != "" && cfg.Todoist.APIToken != "" {
		t.Error("the scaffold must never write a token")
	}
	// The scaffold ships confirm commented out, so the generated file must
	// still resolve to the default rather than silently turning it off.
	if !cfg.ConfirmPick() {
		t.Error("the scaffolded config disables the pick confirmation")
	}
	for _, want := range []string{"TODOIST_API_TOKEN", "Chores 🧹", "Work", "[projects.", "longest path wins", "[pick]", "confirm"} {
		if !strings.Contains(body, want) {
			t.Errorf("scaffold is missing %q", want)
		}
	}
}
