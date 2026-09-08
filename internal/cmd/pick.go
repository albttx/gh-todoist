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
	var projectFlag, repoFlag, queryFlag, assignFlag string
	var revive bool

	cmd := &cobra.Command{
		Use:   "pick",
		Short: "Interactively choose GitHub issues to push to Todoist",
		Long: `Present open GitHub issues and push the ones you select.

Inside a repository the default pool is every open issue in it. Outside one,
"every open issue on GitHub" is not a pool anyone can curate, so the default
narrows to issues assigned to you.

Use --assign to filter by assignee, --repo to point at another repository, or
--query to replace the pool with a raw GitHub search.

Issues already tracked in Todoist are hidden from the list, so what you see is
what is not yet on your Todoist plate.`,
		Args: pickArgs,
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

			// Resolve the repository here rather than inside the query builder,
			// so the builder stays a pure function of its arguments.
			repo := strings.TrimSpace(repoFlag)
			if repo == "" {
				if current, ok := ghsrc.CurrentRepo(); ok {
					repo = current.Owner + "/" + current.Repo
				}
			}
			// pflag will not consume a space-separated value for a flag carrying
			// NoOptDefVal, so `--assign alice` arrives as a bare --assign plus a
			// stray argument. pickArgs allows exactly that shape through; fold it
			// back in here so both --assign=alice and --assign alice work.
			assign := assignFlag
			if len(args) == 1 {
				assign = args[0]
			}
			query, scope := buildSearchQuery(queryFlag, repo, assign)
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
				// Submitting now falls back to the highlighted issue, so an
				// empty result means there was nothing to highlight.
				fmt.Fprintln(out, "No issue highlighted; nothing to push.")
				return nil
			}

			// [pick].confirm = false pushes straight from the picker. The
			// summary below still prints either way, since with the screen gone
			// it is the only feedback that anything happened.
			if a.cfg.ConfirmPick() {
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
	cmd.Flags().StringVar(&assignFlag, "assign", "",
		"only issues assigned to this user; bare --assign means yourself. "+
			"Outside a repository this is the default, since an unfiltered pool would be every issue on GitHub")
	// NoOptDefVal makes the bare --assign form work. It also means pflag will
	// not consume a following space-separated word, so a value must be attached
	// as --assign=alice; checkPickArgs turns the resulting confusion into a
	// pointed error.
	cmd.Flags().Lookup("assign").NoOptDefVal = "me"
	cmd.Flags().StringVar(&queryFlag, "query", "", "raw GitHub search query, replacing the default pool")
	cmd.Flags().BoolVar(&revive, "revive", false, "allow re-adding issues that were previously tombstoned")
	return cmd
}

// pickArgs accepts no arguments, with one exception: a flag carrying
// NoOptDefVal cannot absorb a space-separated value, so `--assign alice` is
// parsed as a bare --assign followed by the stray argument "alice". Letting
// that single case through keeps the natural spelling working instead of
// failing with cobra's opaque `unknown command "alice"`.
func pickArgs(c *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if len(args) == 1 && c.Flags().Changed("assign") {
		return nil
	}
	return fmt.Errorf("pick takes no arguments, got %q", strings.Join(args, " "))
}

// buildSearchQuery derives the GitHub search query and a human description of
// the pool it covers.
//
// repo is the already-resolved owner/repo, empty when there is no repository
// context at all. assign is the raw --assign value, empty when the flag was not
// given. rawQuery replaces everything.
func buildSearchQuery(rawQuery, repo, assign string) (query, scope string) {
	if q := strings.TrimSpace(rawQuery); q != "" {
		return q, fmt.Sprintf("query %q", q)
	}

	repo = strings.TrimSpace(repo)
	assignee := normalizeAssignee(assign)

	// Inside a repository, every open issue is a curatable pool. Outside one it
	// would be every open issue on GitHub, which is not, so fall back to the
	// issues assigned to you.
	if repo == "" && assignee == "" {
		assignee = "@me"
	}

	terms := []string{"is:open", "is:issue"}
	if repo != "" {
		terms = append(terms, "repo:"+repo)
	}
	if assignee != "" {
		terms = append(terms, "assignee:"+assignee)
	}
	return strings.Join(terms, " "), describePool(repo, assignee)
}

// normalizeAssignee turns the --assign value into a GitHub search qualifier.
// The bare flag yields "me" through NoOptDefVal, which GitHub spells "@me".
func normalizeAssignee(assign string) string {
	switch assign = strings.TrimSpace(assign); assign {
	case "":
		return ""
	case "me", "@me":
		return "@me"
	default:
		return strings.TrimPrefix(assign, "@")
	}
}

// describePool renders the pool for the "No open issues matched ..." message
// and the picker header.
func describePool(repo, assignee string) string {
	who := ""
	switch assignee {
	case "":
	case "@me":
		who = " assigned to you"
	default:
		who = " assigned to " + assignee
	}
	if repo != "" {
		return "open issues in " + repo + who
	}
	return "open issues" + who
}
