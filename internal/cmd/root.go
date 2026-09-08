package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// version is overridden at build time via -ldflags.
var version = "dev"

// NewRoot builds the command tree. out and errOut are injectable so the tree
// can be exercised without touching the process streams.
func NewRoot(out, errOut io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:   "gh-todoist",
		Short: "Push hand-picked GitHub issues into Todoist",
		Long: `gh-todoist pushes GitHub issues you choose into Todoist as tasks.

The sync is strictly one-way. GitHub is the source of truth and this tool never
writes to GitHub: it does not close issues, comment, label, or assign. The only
thing it changes is your Todoist.

Typical use:

  gh todoist init            # write ~/.config/gh-todoist/config.toml
  gh todoist pick            # choose from your assigned open issues
  gh todoist add 812         # push one issue from the current repo
  gh todoist sync            # complete tasks whose issues closed on GitHub
  gh todoist status          # read-only drift report

Tasks are created with the label "gh" (configurable), and a task is joined back
to its issue through local state, that label, and the issue URL embedded in the
task title.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	root.SetOut(out)
	root.SetErr(errOut)

	root.AddCommand(
		newAddCmd(out, errOut),
		newPickCmd(out, errOut),
		newSyncCmd(out, errOut),
		newStatusCmd(out, errOut),
		newInitCmd(out, errOut),
	)
	return root
}

// ReportError prints a top-level failure. Errors carry their own context via
// wrapping, so no extra decoration is added beyond the prefix.
func ReportError(w io.Writer, err error) {
	fmt.Fprintf(w, "error: %v\n", err)
}
