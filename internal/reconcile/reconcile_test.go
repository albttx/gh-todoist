package reconcile

import (
	"reflect"
	"testing"
)

const (
	urlA = "https://github.com/o/r/issues/1"
	urlB = "https://github.com/o/r/issues/2"
	urlC = "https://github.com/o/r/issues/3"
)

func task(id, url string) Task {
	return Task{ID: id, Content: "[o/r#n](" + url + ") title"}
}

func TestCompute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   Input
		want Plan
	}{
		{
			name: "empty input yields an empty plan",
			in:   Input{},
			want: Plan{},
		},
		{
			name: "open on both sides is left alone",
			in: Input{
				Tracked:   []Tracked{{IssueURL: urlA, TodoistID: "t1"}},
				OpenTasks: []Task{task("t1", urlA)},
				GitHub:    map[string]GitHubState{urlA: {State: "open"}},
			},
			want: Plan{OpenInBoth: 1},
		},
		{
			name: "closed on GitHub queues a close",
			in: Input{
				Tracked:   []Tracked{{IssueURL: urlA, TodoistID: "t1"}},
				OpenTasks: []Task{task("t1", urlA)},
				GitHub:    map[string]GitHubState{urlA: {State: "closed", ClosedAt: "2026-09-01T00:00:00Z"}},
			},
			want: Plan{Close: []Close{{IssueURL: urlA, TaskID: "t1", ClosedAt: "2026-09-01T00:00:00Z"}}},
		},
		{
			name: "a merged pull request counts as closed",
			in: Input{
				Tracked:   []Tracked{{IssueURL: urlA, TodoistID: "t1"}},
				OpenTasks: []Task{task("t1", urlA)},
				GitHub:    map[string]GitHubState{urlA: {State: "merged", ClosedAt: "2026-09-01T00:00:00Z"}},
			},
			want: Plan{Close: []Close{{IssueURL: urlA, TaskID: "t1", ClosedAt: "2026-09-01T00:00:00Z"}}},
		},
		{
			name: "task gone from Todoist is tombstoned, not closed",
			in: Input{
				Tracked: []Tracked{{IssueURL: urlA, TodoistID: "t1"}},
				// The label listing no longer carries t1: it was completed or
				// deleted in Todoist.
				OpenTasks: nil,
				GitHub:    map[string]GitHubState{urlA: {State: "open"}},
			},
			want: Plan{TombstoneCompleted: []string{urlA}},
		},
		{
			name: "task gone and issue closed still only tombstones",
			in: Input{
				Tracked:   []Tracked{{IssueURL: urlA, TodoistID: "t1"}},
				OpenTasks: nil,
				GitHub:    map[string]GitHubState{urlA: {State: "closed"}},
			},
			want: Plan{TombstoneCompleted: []string{urlA}},
		},
		{
			name: "already tombstoned entries are counted and skipped",
			in: Input{
				Tracked:   []Tracked{{IssueURL: urlA, TodoistID: "t1", Tombstoned: true}},
				OpenTasks: nil,
				GitHub:    map[string]GitHubState{urlA: {State: "closed"}},
			},
			want: Plan{Tombstoned: 1},
		},
		{
			name: "tombstoned but reopened is reported, never auto-revived",
			in: Input{
				Tracked:   []Tracked{{IssueURL: urlA, TodoistID: "t1", Tombstoned: true}},
				OpenTasks: nil,
				GitHub:    map[string]GitHubState{urlA: {State: "open"}},
			},
			want: Plan{Tombstoned: 1, ReopenedTombstoned: []string{urlA}},
		},
		{
			name: "unknown GitHub state draws no conclusion",
			in: Input{
				Tracked:   []Tracked{{IssueURL: urlA, TodoistID: "t1"}},
				OpenTasks: []Task{task("t1", urlA)},
				GitHub:    map[string]GitHubState{},
			},
			want: Plan{UnknownOnGitHub: []string{urlA}},
		},
		{
			name: "missing task id is recovered from task content",
			in: Input{
				Tracked:   []Tracked{{IssueURL: urlA}},
				OpenTasks: []Task{task("recovered", urlA)},
				GitHub:    map[string]GitHubState{urlA: {State: "open"}},
			},
			want: Plan{
				OpenInBoth:   1,
				RecoveredIDs: map[string]string{urlA: "recovered"},
			},
		},
		{
			name: "missing task id with no matching task is tombstoned",
			in: Input{
				Tracked:   []Tracked{{IssueURL: urlA}},
				OpenTasks: []Task{task("other", urlB)},
				GitHub:    map[string]GitHubState{urlA: {State: "open"}},
			},
			want: Plan{TombstoneCompleted: []string{urlA}},
		},
		{
			name: "recovered id still drives a close",
			in: Input{
				Tracked:   []Tracked{{IssueURL: urlA}},
				OpenTasks: []Task{task("recovered", urlA)},
				GitHub:    map[string]GitHubState{urlA: {State: "closed", ClosedAt: "2026-02-02T00:00:00Z"}},
			},
			want: Plan{
				Close:        []Close{{IssueURL: urlA, TaskID: "recovered", ClosedAt: "2026-02-02T00:00:00Z"}},
				RecoveredIDs: map[string]string{urlA: "recovered"},
			},
		},
		{
			name: "mixed population",
			in: Input{
				Tracked: []Tracked{
					{IssueURL: urlA, TodoistID: "t1"},
					{IssueURL: urlB, TodoistID: "t2"},
					{IssueURL: urlC, TodoistID: "t3"},
				},
				OpenTasks: []Task{task("t1", urlA), task("t2", urlB)},
				GitHub: map[string]GitHubState{
					urlA: {State: "open"},
					urlB: {State: "closed", ClosedAt: "2026-03-03T00:00:00Z"},
					urlC: {State: "open"},
				},
			},
			want: Plan{
				OpenInBoth:         1,
				Close:              []Close{{IssueURL: urlB, TaskID: "t2", ClosedAt: "2026-03-03T00:00:00Z"}},
				TombstoneCompleted: []string{urlC},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Compute(tt.in)
			// Compute always allocates the recovery map; normalise the empty
			// case so the table stays readable.
			if len(got.RecoveredIDs) == 0 {
				got.RecoveredIDs = nil
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Compute() =\n  %+v\nwant\n  %+v", got, tt.want)
			}
		})
	}
}

// TestComputeNeverResurrects is the invariant the whole tombstone mechanism
// exists to protect: an issue whose task you completed in Todoist must never
// reappear, even though it is still open on GitHub.
func TestComputeNeverResurrects(t *testing.T) {
	t.Parallel()
	in := Input{
		Tracked:   []Tracked{{IssueURL: urlA, TodoistID: "t1", Tombstoned: true}},
		OpenTasks: nil,
		GitHub:    map[string]GitHubState{urlA: {State: "open"}},
	}
	got := Compute(in)
	if len(got.Close) != 0 || len(got.TombstoneCompleted) != 0 {
		t.Errorf("a tombstoned issue produced actions: %+v", got)
	}
}

func TestRecoveryDoesNotMatchAPrefixIssueNumber(t *testing.T) {
	t.Parallel()
	// Issue 81's URL is a prefix of issue 812's. Content matching must not
	// confuse them, or sync would complete the wrong task.
	url81 := "https://github.com/o/r/issues/81"
	url812 := "https://github.com/o/r/issues/812"
	tasks := []Task{task("task812", url812)}

	got := Compute(Input{
		Tracked:   []Tracked{{IssueURL: url81}},
		OpenTasks: tasks,
		GitHub:    map[string]GitHubState{url81: {State: "open"}},
	})
	if id, ok := got.RecoveredIDs[url81]; ok {
		t.Fatalf("issue 81 wrongly recovered task %q belonging to issue 812", id)
	}
	if len(got.TombstoneCompleted) != 1 {
		t.Errorf("expected issue 81 to be treated as gone, got %+v", got)
	}
}

func TestGitHubStateHelpers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state      string
		wantKnown  bool
		wantClosed bool
	}{
		{state: "", wantKnown: false, wantClosed: false},
		{state: "open", wantKnown: true, wantClosed: false},
		{state: "OPEN", wantKnown: true, wantClosed: false},
		{state: "closed", wantKnown: true, wantClosed: true},
		{state: "CLOSED", wantKnown: true, wantClosed: true},
		{state: "merged", wantKnown: true, wantClosed: true},
	}
	for _, tt := range tests {
		s := GitHubState{State: tt.state}
		if s.Known() != tt.wantKnown {
			t.Errorf("GitHubState{%q}.Known() = %v", tt.state, s.Known())
		}
		if s.Closed() != tt.wantClosed {
			t.Errorf("GitHubState{%q}.Closed() = %v", tt.state, s.Closed())
		}
	}
}

func TestSortTracked(t *testing.T) {
	t.Parallel()
	tracked := []Tracked{{IssueURL: urlC}, {IssueURL: urlA}, {IssueURL: urlB}}
	SortTracked(tracked)
	want := []string{urlA, urlB, urlC}
	for i, w := range want {
		if tracked[i].IssueURL != w {
			t.Errorf("tracked[%d] = %s, want %s", i, tracked[i].IssueURL, w)
		}
	}
}

// TestComputeIsDeterministic guards the ordering contract: the same input must
// always yield the same plan, so output and state writes are reproducible.
func TestComputeIsDeterministic(t *testing.T) {
	t.Parallel()
	in := Input{
		Tracked: []Tracked{
			{IssueURL: urlA, TodoistID: "t1"},
			{IssueURL: urlB, TodoistID: "t2"},
			{IssueURL: urlC, TodoistID: "t3"},
		},
		OpenTasks: []Task{task("t1", urlA)},
		GitHub: map[string]GitHubState{
			urlA: {State: "closed"},
			urlB: {State: "open"},
			urlC: {State: "open"},
		},
	}
	first := Compute(in)
	for range 20 {
		if !reflect.DeepEqual(Compute(in), first) {
			t.Fatal("Compute is not deterministic across runs")
		}
	}
}
