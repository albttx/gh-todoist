package todoist

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestListTasksByLabelPaginatesAndDeduplicates(t *testing.T) {
	t.Parallel()

	page := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks" {
			t.Errorf("path = %q, want /tasks", r.URL.Path)
		}
		if got := r.URL.Query().Get("label"); got != "gh" {
			t.Errorf("label = %q, want gh", got)
		}
		if got := r.URL.Query().Get("limit"); got != "200" {
			t.Errorf("limit = %q, want the 200 maximum", got)
		}
		// The filter-query syntax is being retired; the label parameter is the
		// supported route and must be what we send.
		if q := r.URL.Query().Get("filter"); q != "" {
			t.Errorf("must not use the filter endpoint syntax, got %q", q)
		}

		page++
		switch page {
		case 1:
			if c := r.URL.Query().Get("cursor"); c != "" {
				t.Errorf("first page sent cursor %q", c)
			}
			fmt.Fprint(w, `{"results":[{"id":"a","content":"one"},{"id":"b","content":"two"}],"next_cursor":"CUR"}`)
		case 2:
			if c := r.URL.Query().Get("cursor"); c != "CUR" {
				t.Errorf("second page cursor = %q, want CUR", c)
			}
			// "b" repeats: Todoist warns pagination can duplicate under
			// concurrent edits, so the client must collapse it.
			fmt.Fprint(w, `{"results":[{"id":"b","content":"two"},{"id":"c","content":"three"}],"next_cursor":null}`)
		default:
			t.Fatalf("unexpected extra page request %d", page)
		}
	})

	tasks, err := client.ListTasksByLabel(context.Background(), "gh")
	if err != nil {
		t.Fatalf("ListTasksByLabel() error = %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("got %d tasks, want 3 after deduplication: %+v", len(tasks), tasks)
	}
	want := []string{"a", "b", "c"}
	for i, id := range want {
		if tasks[i].ID != id {
			t.Errorf("tasks[%d].ID = %q, want %q", i, tasks[i].ID, id)
		}
	}
}

func TestListProjectsPaginates(t *testing.T) {
	t.Parallel()
	page := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		page++
		if page == 1 {
			fmt.Fprint(w, `{"results":[{"id":"1","name":"Inbox","is_inbox_project":true}],"next_cursor":"N"}`)
			return
		}
		fmt.Fprint(w, `{"results":[{"id":"proj123","name":"Chores 🧹"}],"next_cursor":""}`)
	})

	projects, err := client.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(projects))
	}
	byName := ProjectsByName(projects)
	if byName["Chores 🧹"] != "proj123" {
		t.Errorf("name lookup failed for a project with emoji: %v", byName)
	}
}

func TestListStopsOnRepeatedCursor(t *testing.T) {
	t.Parallel()
	calls := 0
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		// A server that never advances the cursor would otherwise loop forever.
		fmt.Fprint(w, `{"results":[{"id":"a"}],"next_cursor":"SAME"}`)
	})
	if _, err := client.ListTasksByLabel(context.Background(), "gh"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls > 2 {
		t.Errorf("made %d requests, want the loop to break once the cursor repeats", calls)
	}
}

func TestProjectsByNameKeepsFirstDuplicate(t *testing.T) {
	t.Parallel()
	byName := ProjectsByName([]Project{
		{ID: "first", Name: "Work"},
		{ID: "second", Name: "Work"},
	})
	if byName["Work"] != "first" {
		t.Errorf("got %q, want the first project of a duplicated name", byName["Work"])
	}
}
