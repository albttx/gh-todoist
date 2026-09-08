package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/albttx/gh-todoist/internal/tui"
	"github.com/albttx/gh-todoist/pkg/ghsrc"
	"github.com/spf13/cobra"
)

// pickLimit caps how many candidates the picker will load. Beyond this the list
// stops being something a human can curate, which is the whole point of pick.
const pickLimit = 200

func newPickCmd(out, errOut io.Writer) *cobra.Command {
	var projectFlag, repoFlag, queryFlag string
	var revive bool

	cmd := &cobra.Command{
		Use:   "pick",
		Short: "Interactively choose GitHub issues to push to Todoist",
		Long: `Present your open GitHub issues and push the ones you select.

The default pool is issues assigned to you: scoped to the current repository
when the working directory is a checkout, and across all repositories
otherwise. --repo and --query override that.

Issues already tracked in Todoist are hidden from the list, so what you see is
what is not yet on your Todoist plate.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			a, err := newApp(out, errOut)
			if err != nil {
				return err
			}
			// Check the Todoist token before searching GitHub, so a missing
			// token fails immediately rather than after a slow search.
			if _, err := a.todoistClient(); err != nil {
				return err
			}
			gh, err := a.githubClient()
			if err != nil {
				return err
			}

			query, scope := buildSearchQuery(queryFlag, repoFlag)
			candidates, err := gh.SearchIssues(ctx, query, pickLimit)
			if err != nil {
				return err
			}
			if len(candidates) == 0 {
				fmt.Fprintf(out, "No open issues matched %s.\n", scope)
				return nil
			}

			// Hide what is already on the Todoist plate; report the count so the
			// absence is explained rather than mysterious.
			var pool []ghsrc.Issue
			hidden := 0
			for _, issue := range candidates {
				if rec, tracked := a.st.Get(issue.URL()); tracked && !rec.Tombstoned {
					hidden++
					continue
				}
				pool = append(pool, issue)
			}
			ghsrc.SortIssues(pool)

			project, err := a.resolveProject(ctx, a.cfg.ResolveProject(projectFlag, a.cwd))
			if err != nil {
				return err
			}

			if hidden > 0 {
				fmt.Fprintf(out, "%d already tracked, hidden\n", hidden)
			}
			if len(pool) == 0 {
				fmt.Fprintln(out, "Nothing left to pick.")
				return nil
			}

			title := fmt.Sprintf("Select issues to push (%s)", scope)
			desc := fmt.Sprintf("Todoist project: %s — from %s", project.Display, project.Source)
			picked, err := tui.SelectIssues(title, desc, pool)
			if err != nil {
				if errors.Is(err, tui.ErrAborted) {
					fmt.Fprintln(out, "Aborted; nothing was pushed.")
					return nil
				}
				return err
			}
			if len(picked) == 0 {
				fmt.Fprintln(out, "Nothing selected.")
				return nil
			}

			confirmed, err := tui.ConfirmPush(len(picked), project.Display)
			if err != nil {
				if errors.Is(err, tui.ErrAborted) {
					fmt.Fprintln(out, "Aborted; nothing was pushed.")
					return nil
				}
				return err
			}
			if !confirmed {
				fmt.Fprintln(out, "Cancelled; nothing was pushed.")
				return nil
			}

			results, pushErr := a.push(ctx, picked, pushOptions{Project: project, Revive: revive})
			failures := a.printPushResults(results)
			if pushErr != nil {
				return pushErr
			}
			if failures > 0 {
				return fmt.Errorf("%d of %d issues could not be pushed", failures, len(results))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&projectFlag, "project", "", "Todoist project name or id (overrides config)")
	cmd.Flags().StringVar(&repoFlag, "repo", "", "restrict the candidate pool to owner/repo")
	cmd.Flags().StringVar(&queryFlag, "query", "", "raw GitHub search query, replacing the default pool")
	cmd.Flags().BoolVar(&revive, "revive", false, "allow re-adding issues that were previously tombstoned")
	return cmd
}

// buildSearchQuery derives the GitHub search query and a human description of
// the pool it covers.
func buildSearchQuery(rawQuery, repo string) (query, scope string) {
	if q := strings.TrimSpace(rawQuery); q != "" {
		return q, fmt.Sprintf("query %q", q)
	}
	base := "assignee:@me is:open is:issue"
	if repo = strings.TrimSpace(repo); repo != "" {
		return base + " repo:" + repo, "assigned to you in " + repo
	}
	if current, ok := ghsrc.CurrentRepo(); ok {
		r := current.Owner + "/" + current.Repo
		return base + " repo:" + r, "assigned to you in " + r
	}
	return base, "assigned to you"
}
