// Package reconcile contains the one-way diff engine: given what the local
// state believes, what Todoist currently holds, and what GitHub reports, it
// decides which tasks to complete and which issues to tombstone.
//
// Everything here is a pure function of its inputs. It performs no I/O, which
// is what makes the interesting behaviour table-testable.
package reconcile

import (
	"sort"
	"strings"
)

// Tracked is one entry of the local join table.
type Tracked struct {
	IssueURL string
	// TodoistID may be empty when the create command was a server-side
	// idempotent no-op and Todoist returned no temp_id mapping for it.
	TodoistID  string
	Tombstoned bool
}

// Task is a Todoist task as seen in the label listing. Only the fields the diff
// needs are modelled.
type Task struct {
	ID      string
	Content string
}

// GitHubState is the current state of an issue on GitHub. An empty State means
// GitHub did not report on this issue and no conclusion may be drawn.
type GitHubState struct {
	State    string // "open", "closed", or "merged"
	ClosedAt string // RFC3339
}

// Closed reports whether the issue is no longer open. A merged pull request
// counts as closed.
func (s GitHubState) Closed() bool {
	switch strings.ToLower(s.State) {
	case "closed", "merged":
		return true
	default:
		return false
	}
}

// Known reports whether GitHub actually told us about this issue.
func (s GitHubState) Known() bool { return s.State != "" }

// Input is everything the diff needs. Tracked is consumed in order, so callers
// should sort it for deterministic output.
type Input struct {
	Tracked []Tracked
	// OpenTasks is the current GET /tasks?label=<label> listing.
	OpenTasks []Task
	// GitHub maps issue URL to state. Missing entries are treated as unknown.
	GitHub map[string]GitHubState
}

// Close is an issue whose GitHub state went closed while its Todoist task is
// still open.
type Close struct {
	IssueURL string
	TaskID   string
	ClosedAt string
}

// Plan is the decision the engine reached. It is advice: applying it is the
// caller's job.
type Plan struct {
	// TombstoneCompleted lists issues whose task disappeared from Todoist,
	// meaning it was completed or deleted there. They must never be re-added.
	TombstoneCompleted []string
	// Close lists the item_close commands to issue, and the tombstones to write
	// once they succeed.
	Close []Close
	// RecoveredIDs maps issue URL to a task id recovered from task content for
	// entries whose stored id was empty.
	RecoveredIDs map[string]string
	// OpenInBoth counts issues open on GitHub with a live Todoist task.
	OpenInBoth int
	// UnknownOnGitHub lists tracked issues GitHub did not report on.
	UnknownOnGitHub []string
	// ReopenedTombstoned lists issues that are open on GitHub again but remain
	// tombstoned locally. Reported only; reviving is an explicit user action.
	ReopenedTombstoned []string
	// Tombstoned counts entries already tombstoned before this run.
	Tombstoned int
}

// Compute produces the plan. It never proposes anything that writes to GitHub.
func Compute(in Input) Plan {
	plan := Plan{RecoveredIDs: map[string]string{}}

	liveIDs := make(map[string]struct{}, len(in.OpenTasks))
	for _, t := range in.OpenTasks {
		liveIDs[t.ID] = struct{}{}
	}

	for _, tr := range in.Tracked {
		gh := in.GitHub[tr.IssueURL]

		if tr.Tombstoned {
			plan.Tombstoned++
			if gh.Known() && !gh.Closed() {
				plan.ReopenedTombstoned = append(plan.ReopenedTombstoned, tr.IssueURL)
			}
			continue
		}

		taskID := tr.TodoistID
		if taskID == "" {
			// The create was an idempotent no-op, so we never learned the id.
			// The canonical issue URL is embedded in the task content, which is
			// the documented last-resort recovery path.
			if recovered, ok := findTaskByIssueURL(in.OpenTasks, tr.IssueURL); ok {
				taskID = recovered
				plan.RecoveredIDs[tr.IssueURL] = recovered
			}
		}

		if taskID == "" {
			// Not in state, not in Todoist: the task is gone.
			plan.TombstoneCompleted = append(plan.TombstoneCompleted, tr.IssueURL)
			continue
		}
		if _, live := liveIDs[taskID]; !live {
			plan.TombstoneCompleted = append(plan.TombstoneCompleted, tr.IssueURL)
			continue
		}

		if !gh.Known() {
			plan.UnknownOnGitHub = append(plan.UnknownOnGitHub, tr.IssueURL)
			continue
		}
		if gh.Closed() {
			plan.Close = append(plan.Close, Close{
				IssueURL: tr.IssueURL,
				TaskID:   taskID,
				ClosedAt: gh.ClosedAt,
			})
			continue
		}
		plan.OpenInBoth++
	}
	return plan
}

// findTaskByIssueURL locates a task whose content embeds the canonical issue
// URL. Matching is exact-substring on the URL, so issue 81 cannot match a task
// for issue 812: the URL is followed by ")" in the markdown link.
func findTaskByIssueURL(tasks []Task, issueURL string) (string, bool) {
	needle := issueURL + ")"
	for _, t := range tasks {
		if strings.Contains(t.Content, needle) {
			return t.ID, true
		}
	}
	return "", false
}

// SortTracked orders entries by issue URL so Compute's output is stable.
func SortTracked(tracked []Tracked) {
	sort.Slice(tracked, func(i, j int) bool { return tracked[i].IssueURL < tracked[j].IssueURL })
}
