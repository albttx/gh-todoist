package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/albttx/gh-todoist/pkg/ghsrc"
	"github.com/albttx/gh-todoist/pkg/todoist"
)

// pushOutcome classifies what happened to one issue in a push.
type pushOutcome int

const (
	outcomeAdded pushOutcome = iota
	outcomeAlreadyTracked
	outcomeTombstoned
	outcomeFailed
)

// pushResult is the per-issue result of a push, in input order.
type pushResult struct {
	Issue   ghsrc.Issue
	Outcome pushOutcome
	Detail  string
}

// pushOptions controls a push.
type pushOptions struct {
	Project resolvedProject
	Revive  bool
}

// push sends the given issues to Todoist in a single batched /sync call,
// skipping issues already tracked or tombstoned, then persists state.
//
// Every command uuid in the response is inspected individually: /sync batches
// are not transactional, and trusting the HTTP status alone would silently
// desynchronise state.json.
func (a *app) push(ctx context.Context, issues []ghsrc.Issue, opts pushOptions) ([]pushResult, error) {
	results := make([]pushResult, 0, len(issues))
	var commands []todoist.Command
	// queued maps command uuid back to the index in results awaiting an answer.
	queued := map[string]int{}
	tempIDs := map[string]string{}

	// The same issue can legitimately be named twice (`add 812 812`, or a repo
	// appearing under two search pages). Both would derive the same command
	// uuid, so the second would overwrite the first in `queued` and be reported
	// as a spurious failure. Collapse them up front.
	seen := make(map[string]struct{}, len(issues))
	// reviving records issues whose tombstone must be cleared, but only once
	// Todoist confirms the create. Clearing it up front would leave a failed
	// revive looking "already tracked" with no task behind it, and --revive
	// would not help because that path requires the tombstone to still be set.
	reviving := map[string]bool{}

	for _, issue := range issues {
		url := issue.URL()
		if _, dup := seen[url]; dup {
			continue
		}
		seen[url] = struct{}{}

		rec, tracked := a.st.Get(url)

		switch {
		case tracked && !rec.Tombstoned:
			results = append(results, pushResult{Issue: issue, Outcome: outcomeAlreadyTracked})
			continue
		case tracked && rec.Tombstoned && !opts.Revive:
			results = append(results, pushResult{
				Issue:   issue,
				Outcome: outcomeTombstoned,
				Detail:  "use --revive",
			})
			continue
		case tracked && rec.Tombstoned && opts.Revive:
			reviving[url] = true
		}

		// A revive must carry an idempotency key Todoist has not executed, so it
		// is built against the next generation. The state only moves there if
		// the command succeeds.
		generation := rec.ReviveCount
		if reviving[url] {
			generation++
		}

		cmd := todoist.BuildItemAdd(todoist.IssueInput{
			URL:          url,
			Ref:          issue.Ref.String(),
			Title:        issue.Title,
			Body:         issue.Body,
			Labels:       issue.Labels,
			MilestoneDue: issue.MilestoneDue,
			Generation:   generation,
		}, opts.Project.ID, a.cfg.Label())

		idx := len(results)
		results = append(results, pushResult{Issue: issue, Outcome: outcomeFailed, Detail: "no response"})
		queued[cmd.UUID] = idx
		tempIDs[cmd.UUID] = cmd.TempID
		commands = append(commands, cmd)
	}

	if len(commands) == 0 {
		return results, nil
	}

	td, err := a.todoistClient()
	if err != nil {
		return results, err
	}
	res, syncErr := td.Sync(ctx, commands)
	now := time.Now().UTC()

	for uuid, idx := range queued {
		status, answered := res.Status[uuid]
		if !answered {
			results[idx].Outcome = outcomeFailed
			results[idx].Detail = "no status returned"
			continue
		}
		if status != nil {
			results[idx].Outcome = outcomeFailed
			results[idx].Detail = status.Error
			continue
		}
		// Success. A uuid Todoist had already executed returns ok but carries no
		// temp_id mapping; record the issue as tracked with an empty task id and
		// let `sync` recover the id from the label listing.
		taskID := res.TempIDMapping[tempIDs[uuid]]
		url := results[idx].Issue.URL()
		if reviving[url] {
			// Advances ReviveCount to the generation the command was built with.
			a.st.Revive(url)
		}
		a.st.Track(url, taskID, now)
		results[idx].Outcome = outcomeAdded
		results[idx].Detail = ""
		if taskID == "" {
			results[idx].Detail = "already existed in Todoist; task id recovered on next sync"
		}
	}

	if err := a.st.Save(); err != nil {
		return results, err
	}
	if syncErr != nil {
		return results, syncErr
	}
	return results, nil
}

// printPushResults writes one line per issue and returns the number of failures.
func (a *app) printPushResults(results []pushResult) int {
	failures := 0
	for _, r := range results {
		var marker, note string
		switch r.Outcome {
		case outcomeAdded:
			marker = "✓ added"
			note = r.Detail
		case outcomeAlreadyTracked:
			marker = "• already tracked"
		case outcomeTombstoned:
			marker = "⊘ tombstoned"
			note = r.Detail
		case outcomeFailed:
			marker = "✗ failed"
			note = r.Detail
			failures++
		}
		line := fmt.Sprintf("%-18s %s", marker, r.Issue.Ref)
		if title := r.Issue.Title; title != "" {
			line += "  " + title
		}
		if note != "" {
			line += fmt.Sprintf("  (%s)", note)
		}
		fmt.Fprintln(a.out, line)
	}
	return failures
}
