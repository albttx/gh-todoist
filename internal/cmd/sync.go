package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/albttx/gh-todoist/internal/reconcile"
	"github.com/albttx/gh-todoist/pkg/ghsrc"
	"github.com/albttx/gh-todoist/pkg/todoist"
	"github.com/spf13/cobra"
)

func newSyncCmd(out, errOut io.Writer) *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Reconcile tracked issues with Todoist (one-way)",
		Long: `Bring Todoist back in line with GitHub.

Two things happen, in this order:

  1. Tasks that vanished from Todoist (you completed or deleted them) are
     tombstoned locally so they are never resurrected.
  2. Issues that closed on GitHub have their Todoist task completed.

Nothing is ever written to GitHub.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			a, err := newApp(out, errOut)
			if err != nil {
				return err
			}
			return a.runSync(c.Context(), dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would change without writing to Todoist")
	return cmd
}

// gatherPlan performs the read side of reconciliation: the Todoist label
// listing and the batched GitHub state query, fed into the pure diff engine.
func (a *app) gatherPlan(ctx context.Context) (reconcile.Plan, error) {
	td, err := a.todoistClient()
	if err != nil {
		return reconcile.Plan{}, err
	}
	tasks, err := td.ListTasksByLabel(ctx, a.cfg.Label())
	if err != nil {
		return reconcile.Plan{}, err
	}

	tracked := make([]reconcile.Tracked, 0, len(a.st.Issues))
	for url, rec := range a.st.Issues {
		tracked = append(tracked, reconcile.Tracked{
			IssueURL:   url,
			TodoistID:  rec.TodoistID,
			Tombstoned: rec.Tombstoned,
		})
	}
	reconcile.SortTracked(tracked)

	// Only non-tombstoned issues need a GitHub round trip for the close
	// decision, but reopened tombstones are worth reporting, so ask about
	// everything we still know about.
	refs := trackedRefs(a.st, true)
	ghStates := map[string]reconcile.GitHubState{}
	if len(refs) > 0 {
		gh, err := a.githubClient()
		if err != nil {
			return reconcile.Plan{}, err
		}
		states, err := gh.IssueStates(ctx, refs)
		if err != nil {
			// A partial failure still yields usable data; anything else is fatal.
			// Surfacing it matters because an unreported whole-query rejection
			// looks exactly like "every tracked issue was deleted".
			var partial *ghsrc.PartialError
			if !errors.As(err, &partial) {
				return reconcile.Plan{}, err
			}
			fmt.Fprintf(a.errOut, "warning: %v\n", partial)
		}
		for url, s := range states {
			ghStates[url] = reconcile.GitHubState{State: s.State, ClosedAt: s.ClosedAt}
		}
	}

	rtasks := make([]reconcile.Task, 0, len(tasks))
	for _, t := range tasks {
		rtasks = append(rtasks, reconcile.Task{ID: t.ID, Content: t.Content})
	}

	return reconcile.Compute(reconcile.Input{
		Tracked:   tracked,
		OpenTasks: rtasks,
		GitHub:    ghStates,
	}), nil
}

func (a *app) runSync(ctx context.Context, dryRun bool) error {
	if len(a.st.Issues) == 0 {
		fmt.Fprintln(a.out, "Nothing tracked yet. Run `gh todoist pick` or `gh todoist add`.")
		return nil
	}

	plan, err := a.gatherPlan(ctx)
	if err != nil {
		return err
	}

	// Recovered ids first: they make the state usable even if the rest fails.
	for url, id := range plan.RecoveredIDs {
		rec := a.st.Issues[url]
		rec.TodoistID = id
		a.st.Issues[url] = rec
	}

	if dryRun {
		a.printSyncSummary(plan, 0, 0)
		fmt.Fprintln(a.out, "\n(dry run: nothing was written to Todoist)")
		return nil
	}

	for _, url := range plan.TombstoneCompleted {
		a.st.TombstoneCompleted(url)
	}

	closed := 0
	var closeErr error
	if len(plan.Close) > 0 {
		td, err := a.todoistClient()
		if err != nil {
			return err
		}
		commands := make([]todoist.Command, 0, len(plan.Close))
		byUUID := map[string]reconcile.Close{}
		for _, cl := range plan.Close {
			cmd := todoist.NewItemClose(cl.IssueURL, cl.ClosedAt, cl.TaskID)
			commands = append(commands, cmd)
			byUUID[cmd.UUID] = cl
		}

		res, err := td.Sync(ctx, commands)
		closeErr = err
		// Every uuid is checked individually: /sync is not transactional, so a
		// single HTTP 200 says nothing about the individual commands.
		for uuid, cl := range byUUID {
			status, answered := res.Status[uuid]
			switch {
			case !answered:
				fmt.Fprintf(a.errOut, "warning: no status for close of %s\n", cl.IssueURL)
			case status != nil:
				fmt.Fprintf(a.errOut, "warning: could not complete task for %s: %s\n", cl.IssueURL, status.Error)
			default:
				a.st.TombstoneClosed(cl.IssueURL, parseClosedAt(cl.ClosedAt))
				closed++
			}
		}
	}

	if err := a.st.Save(); err != nil {
		return err
	}
	a.printSyncSummary(plan, closed, len(plan.TombstoneCompleted))
	return closeErr
}

// parseClosedAt turns GitHub's RFC3339 closedAt into a time, falling back to
// now when GitHub reported nothing usable.
func parseClosedAt(s string) time.Time {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Now().UTC()
}

func (a *app) printSyncSummary(plan reconcile.Plan, closed, tombstoned int) {
	fmt.Fprintf(a.out, "tracked open in both:      %d\n", plan.OpenInBoth)
	fmt.Fprintf(a.out, "completed in Todoist:      %d (tombstoned)\n", len(plan.TombstoneCompleted))
	fmt.Fprintf(a.out, "closed on GitHub:          %d\n", len(plan.Close))
	if closed > 0 || tombstoned > 0 {
		fmt.Fprintf(a.out, "tasks completed this run:  %d\n", closed)
	}
	if n := len(plan.RecoveredIDs); n > 0 {
		fmt.Fprintf(a.out, "task ids recovered:        %d\n", n)
	}
	if n := len(plan.ReopenedTombstoned); n > 0 {
		fmt.Fprintf(a.out, "reopened but tombstoned:   %d (re-add with `add --revive`)\n", n)
		for _, url := range plan.ReopenedTombstoned {
			fmt.Fprintf(a.out, "  %s\n", url)
		}
	}
	if n := len(plan.UnknownOnGitHub); n > 0 {
		fmt.Fprintf(a.out, "unknown on GitHub:         %d (deleted, moved, or no access)\n", n)
		for _, url := range plan.UnknownOnGitHub {
			fmt.Fprintf(a.out, "  %s\n", url)
		}
	}
}
