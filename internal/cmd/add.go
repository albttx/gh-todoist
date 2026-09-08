package cmd

import (
	"fmt"
	"io"

	"github.com/albttx/gh-todoist/pkg/ghsrc"
	"github.com/spf13/cobra"
)

func newAddCmd(out, errOut io.Writer) *cobra.Command {
	var projectFlag string
	var revive bool

	cmd := &cobra.Command{
		Use:   "add [ref...]",
		Short: "Push specific GitHub issues to Todoist",
		Long: `Push one or more GitHub issues to Todoist as tasks.

A reference is one of:

  812                  issue 812 in the repository of the current directory
  owner/repo#812       issue 812 in an explicit repository
  https://github.com/owner/repo/issues/812

All issues are pushed in a single batched request. Issues already tracked are
skipped, and issues whose task you completed in Todoist stay skipped unless you
pass --revive.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			a, err := newApp(out, errOut)
			if err != nil {
				return err
			}

			// Check the Todoist token before spending GitHub round trips on
			// issues we would not be able to push anyway.
			if _, err := a.todoistClient(); err != nil {
				return err
			}

			fallback, _ := ghsrc.CurrentRepo()
			refs, err := ghsrc.ParseRefs(args, fallback)
			if err != nil {
				return err
			}

			gh, err := a.githubClient()
			if err != nil {
				return err
			}
			issues, err := gh.GetIssues(ctx, refs)
			if err != nil {
				return err
			}

			project, err := a.resolveProject(ctx, a.cfg.ResolveProject(projectFlag, a.cwd))
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Todoist project: %s (%s)\n\n", project.Display, project.Source)

			results, pushErr := a.push(ctx, issues, pushOptions{Project: project, Revive: revive})
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
	cmd.Flags().BoolVar(&revive, "revive", false, "re-add issues that were previously tombstoned")
	return cmd
}
