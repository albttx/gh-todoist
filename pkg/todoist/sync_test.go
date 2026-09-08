package todoist

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const goldenIssueURL = "https://github.com/acme/widgets/issues/812"

// TestCommandUUIDGolden pins the derivation against a fixed fixture URL. The
// values here track the fixture, but the derivation itself must not change:
// these uuids are the idempotency keys of every task already created, so a
// different scheme would duplicate all of them. In particular, Namespace is
// load-bearing and must never be edited.
func TestCommandUUIDGolden(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "create, generation 0",
			got:  CommandUUID(goldenIssueURL),
			want: "dd98f828-968a-5f6e-bd8a-4bf9925e93b2",
		},
		{
			name: "generation 0 equals plain CommandUUID",
			got:  CommandUUIDGen(goldenIssueURL, 0),
			want: "dd98f828-968a-5f6e-bd8a-4bf9925e93b2",
		},
		{
			name: "revive generation 2 differs",
			got:  CommandUUIDGen(goldenIssueURL, 2),
			want: "88fc41bd-e5f0-585b-8fdb-7970d96583c5",
		},
		{
			name: "temp id",
			got:  TempID(goldenIssueURL, 0),
			want: "26f8c639-847e-503d-8f54-aa86484fd243",
		},
		{
			name: "close command",
			got:  CloseCommandUUID(goldenIssueURL, "2026-09-08T10:00:00Z"),
			want: "904a59b7-d714-5d54-867a-13558b280957",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.got != tt.want {
				t.Errorf("got %s, want %s", tt.got, tt.want)
			}
		})
	}
}

func TestCommandUUIDIsDeterministicAndDistinct(t *testing.T) {
	t.Parallel()
	a := CommandUUID(goldenIssueURL)
	if a != CommandUUID(goldenIssueURL) {
		t.Fatal("CommandUUID is not deterministic")
	}
	if b := CommandUUID("https://github.com/acme/widgets/issues/813"); a == b {
		t.Fatal("different issues produced the same uuid")
	}
	// A re-close after a reopen must not be swallowed by idempotency.
	first := CloseCommandUUID(goldenIssueURL, "2026-01-01T00:00:00Z")
	second := CloseCommandUUID(goldenIssueURL, "2026-06-01T00:00:00Z")
	if first == second {
		t.Fatal("closes at different times produced the same uuid")
	}
}

// newTestClient wires a Client to a test server with instant retries.
func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, "test-token", WithSleep(func(time.Duration) {}))
}

// decodeCommands pulls the commands array back out of a form-encoded body.
func decodeCommands(t *testing.T, r *http.Request) []Command {
	t.Helper()
	if err := r.ParseForm(); err != nil {
		t.Fatalf("parse form: %v", err)
	}
	var cmds []Command
	if err := json.Unmarshal([]byte(r.PostForm.Get("commands")), &cmds); err != nil {
		t.Fatalf("decode commands: %v", err)
	}
	return cmds
}

func TestSyncPartialFailure(t *testing.T) {
	t.Parallel()

	okUUID := CommandUUID("https://github.com/o/r/issues/1")
	badUUID := CommandUUID("https://github.com/o/r/issues/2")
	dupUUID := CommandUUID("https://github.com/o/r/issues/3")

	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Path != "/sync" {
			t.Errorf("path = %q, want /sync", r.URL.Path)
		}
		cmds := decodeCommands(t, r)
		if len(cmds) != 3 {
			t.Fatalf("got %d commands, want 3", len(cmds))
		}
		fmt.Fprintf(w, `{
		  "sync_status": {
		    %q: "ok",
		    %q: {"error": "Invalid project id", "error_code": 400, "error_tag": "PROJECT_NOT_FOUND"},
		    %q: "ok"
		  },
		  "temp_id_mapping": {%q: "task0000000001"}
		}`, okUUID, badUUID, dupUUID, TempID("https://github.com/o/r/issues/1", 0))
	})

	cmds := []Command{
		NewItemAdd("https://github.com/o/r/issues/1", 0, ItemAddArgs{Content: "one"}),
		NewItemAdd("https://github.com/o/r/issues/2", 0, ItemAddArgs{Content: "two"}),
		NewItemAdd("https://github.com/o/r/issues/3", 0, ItemAddArgs{Content: "three"}),
	}
	res, err := client.Sync(context.Background(), cmds)
	if err != nil {
		t.Fatalf("Sync() error = %v, want nil: a partial failure is not a request failure", err)
	}

	if !res.OK(okUUID) {
		t.Error("expected the first command to be ok")
	}
	failure := res.Err(badUUID)
	if failure == nil {
		t.Fatal("expected the second command to report an error")
	}
	if failure.ErrorCode != 400 || !strings.Contains(failure.Error, "Invalid project id") {
		t.Errorf("unexpected failure detail: %+v", failure)
	}

	// The third command succeeded but Todoist reported no temp_id mapping,
	// which is what an already-executed uuid looks like.
	if !res.OK(dupUUID) {
		t.Error("expected the third command to be ok")
	}
	if id := res.TempIDMapping[TempID("https://github.com/o/r/issues/3", 0)]; id != "" {
		t.Errorf("expected no temp id mapping for the duplicate, got %q", id)
	}
	if id := res.TempIDMapping[TempID("https://github.com/o/r/issues/1", 0)]; id != "task0000000001" {
		t.Errorf("temp id mapping = %q, want the real task id", id)
	}
}

func TestSyncChunksAtHundred(t *testing.T) {
	t.Parallel()

	var batchSizes []int
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		cmds := decodeCommands(t, r)
		batchSizes = append(batchSizes, len(cmds))

		status := map[string]any{}
		mapping := map[string]string{}
		for i, c := range cmds {
			status[c.UUID] = "ok"
			mapping[c.TempID] = fmt.Sprintf("task-%d-%d", len(batchSizes), i)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{
			"sync_status":     status,
			"temp_id_mapping": mapping,
		}); err != nil {
			t.Errorf("encode sync response: %v", err)
		}
	})

	const total = 250
	cmds := make([]Command, 0, total)
	for i := range total {
		url := fmt.Sprintf("https://github.com/o/r/issues/%d", i)
		cmds = append(cmds, NewItemAdd(url, 0, ItemAddArgs{Content: url}))
	}

	res, err := client.Sync(context.Background(), cmds)
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	want := []int{100, 100, 50}
	if len(batchSizes) != len(want) {
		t.Fatalf("got %d requests (%v), want %d", len(batchSizes), batchSizes, len(want))
	}
	for i, n := range want {
		if batchSizes[i] != n {
			t.Errorf("request %d carried %d commands, want %d", i, batchSizes[i], n)
		}
		if batchSizes[i] > MaxCommandsPerRequest {
			t.Errorf("request %d exceeded the server cap of %d", i, MaxCommandsPerRequest)
		}
	}
	if len(res.Status) != total {
		t.Errorf("merged status has %d entries, want %d", len(res.Status), total)
	}
	for _, c := range cmds {
		if !res.OK(c.UUID) {
			t.Fatalf("command %s missing from the merged result", c.UUID)
		}
	}
}

func TestSyncRetriesOnRateLimit(t *testing.T) {
	t.Parallel()

	var slept []time.Duration
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":"Rate limit exceeded","error_extra":{"retry_after":7}}`)
			return
		}
		cmds := decodeCommands(t, r)
		fmt.Fprintf(w, `{"sync_status": {%q: "ok"}, "temp_id_mapping": {}}`, cmds[0].UUID)
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, "tok", WithSleep(func(d time.Duration) { slept = append(slept, d) }))
	cmd := NewItemAdd(goldenIssueURL, 0, ItemAddArgs{Content: "x"})
	res, err := client.Sync(context.Background(), []Command{cmd})
	if err != nil {
		t.Fatalf("Sync() error = %v, want the retry to succeed", err)
	}
	if !res.OK(cmd.UUID) {
		t.Error("command did not succeed after the retry")
	}
	if calls != 2 {
		t.Errorf("made %d requests, want 2 (one retry)", calls)
	}
	if len(slept) != 1 || slept[0] != 7*time.Second {
		t.Errorf("slept %v, want a single 7s wait from error_extra.retry_after", slept)
	}
}

func TestSyncGivesUpAfterOneRetry(t *testing.T) {
	t.Parallel()
	calls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"boom"}`)
	})
	_, err := client.Sync(context.Background(), []Command{
		NewItemAdd(goldenIssueURL, 0, ItemAddArgs{Content: "x"}),
	})
	if err == nil {
		t.Fatal("expected an error after repeated 5xx")
	}
	if calls != 2 {
		t.Errorf("made %d requests, want 2", calls)
	}
}

// TestRetryWaitHonoursCancellation covers Ctrl-C during a rate-limit backoff:
// a retry_after of up to a minute must not make the process unkillable.
func TestRetryWaitHonoursCancellation(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":"Rate limit exceeded","error_extra":{"retry_after":45}}`)
	}))
	t.Cleanup(srv.Close)

	// No sleep hook, so the production context-aware timer is exercised.
	client := New(srv.URL, "tok")
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := client.Sync(ctx, []Command{NewItemAdd(goldenIssueURL, 0, ItemAddArgs{Content: "x"})})
		done <- err
	}()

	// Give the first attempt time to land in the backoff, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error after cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Sync ignored context cancellation and is still waiting out retry_after")
	}
}

func TestItemCloseShape(t *testing.T) {
	t.Parallel()
	cmd := NewItemClose(goldenIssueURL, "2026-09-08T10:00:00Z", "taskABC")
	if cmd.Type != "item_close" {
		t.Errorf("Type = %q", cmd.Type)
	}
	if cmd.TempID != "" {
		t.Error("item_close must not carry a temp_id")
	}
	args, ok := cmd.Args.(map[string]string)
	if !ok || args["id"] != "taskABC" {
		t.Errorf("Args = %#v, want the task id", cmd.Args)
	}
}
