package cmd

import (
	"fmt"
	"io"

	"github.com/albttx/gh-todoist/internal/config"
	"github.com/spf13/cobra"
)

func newStatusCmd(out, errOut io.Writer) *cobra.Command {
	var projectFlag string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show configuration diagnosis and drift, without writing anything",
		Long: `Report what gh-todoist would do, and why.

status changes nothing in GitHub or Todoist. It diagnoses the configuration (is
a token visible, does the configured project resolve, which config layer wins
here) and then reports drift between the tracked issues, their Todoist tasks,
and their GitHub state.

The only thing it may write is the local project-name cache in state.json, if
resolving the project required refreshing it.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			a, err := newApp(out, errOut)
			if err != nil {
				return err
			}

			fmt.Fprintln(out, "Configuration")
			if a.cfg.Path != "" {
				fmt.Fprintf(out, "  config file:      %s\n", a.cfg.Path)
			} else {
				p, _ := config.DefaultPath()
				fmt.Fprintf(out, "  config file:      none (%s) — run `gh todoist init`\n", p)
			}
			fmt.Fprintf(out, "  state file:       %s\n", a.st.Path())
			fmt.Fprintf(out, "  label:            %s\n", a.cfg.Label())

			if src := a.cfg.TokenSource(); src != "" {
				fmt.Fprintf(out, "  todoist token:    found (%s)\n", src)
			} else {
				fmt.Fprintf(out, "  todoist token:    MISSING — set %s or [todoist].api_token\n", config.EnvToken)
			}

			ref := a.cfg.ResolveProject(projectFlag, a.cwd)
			fmt.Fprintf(out, "  project (cwd):    %s — from %s\n", ref.String(), ref.Source)
			if name, entry, ok := a.cfg.MatchDir(a.cwd); ok {
				fmt.Fprintf(out, "  matched mapping:  [projects.%s] path=%s\n", name, config.ExpandPath(entry.Path))
			}

			tracked, tombstoned := a.st.Counts()
			fmt.Fprintf(out, "\nTracked issues\n  total:            %d\n  tombstoned:       %d\n",
				tracked, tombstoned)

			if a.cfg.Token() == "" {
				fmt.Fprintln(out, "\nSkipping live drift report: no Todoist token.")
				return nil
			}

			project, err := a.resolveProject(ctx, ref)
			if err != nil {
				fmt.Fprintf(out, "\nProject resolution FAILED:\n%v\n", err)
				return nil
			}
			fmt.Fprintf(out, "  resolves to:      %s (id %s)\n", project.Display, orNone(project.ID))

			if tracked == 0 {
				return nil
			}

			plan, err := a.gatherPlan(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintln(out, "\nDrift")
			fmt.Fprintf(out, "  open in both:                    %d\n", plan.OpenInBoth)
			fmt.Fprintf(out, "  closed on GitHub, task open:     %d  (sync would complete these)\n", len(plan.Close))
			fmt.Fprintf(out, "  gone from Todoist, not yet dead: %d  (sync would tombstone these)\n", len(plan.TombstoneCompleted))
			fmt.Fprintf(out, "  reopened but tombstoned:         %d  (re-add with `add --revive`)\n", len(plan.ReopenedTombstoned))
			fmt.Fprintf(out, "  unknown on GitHub:               %d\n", len(plan.UnknownOnGitHub))

			for _, cl := range plan.Close {
				fmt.Fprintf(out, "    would close: %s\n", cl.IssueURL)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&projectFlag, "project", "", "diagnose resolution of this project name or id")
	return cmd
}

func orNone(s string) string {
	if s == "" {
		return "inbox"
	}
	return s
}
