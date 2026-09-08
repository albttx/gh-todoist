package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/albttx/gh-todoist/internal/config"
	"github.com/albttx/gh-todoist/pkg/todoist"
	"github.com/spf13/cobra"
)

func newInitCmd(out, errOut io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create ~/.config/gh-todoist/config.toml",
		Long: `Write a starter config.toml.

An existing file is never overwritten; init prints its path and exits. When a
Todoist token is available, your live project names are fetched and included as
comments so the file is quick to fill in.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			path, err := config.DefaultPath()
			if err != nil {
				return err
			}
			if _, err := os.Stat(path); err == nil {
				fmt.Fprintf(out, "Config already exists, leaving it alone:\n  %s\n", path)
				return nil
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("stat %s: %w", path, err)
			}

			a, err := newApp(out, errOut)
			if err != nil {
				return err
			}

			var projectNames []string
			if a.cfg.Token() != "" {
				td, err := a.todoistClient()
				if err == nil {
					projects, err := td.ListProjects(ctx)
					if err != nil {
						fmt.Fprintf(errOut, "warning: could not list Todoist projects: %v\n", err)
					} else {
						projectNames = todoist.ProjectNames(projects)
						sort.Strings(projectNames)
					}
				}
			} else {
				fmt.Fprintf(errOut, "note: %s not set, so live project names are not included\n", config.EnvToken)
			}

			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return fmt.Errorf("create config dir: %w", err)
			}
			// 0600: the file may hold an API token.
			if err := os.WriteFile(path, []byte(scaffold(projectNames)), 0o600); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			fmt.Fprintf(out, "Wrote %s\n", path)
			if len(projectNames) > 0 {
				fmt.Fprintf(out, "Seeded with %d live Todoist project names as comments.\n", len(projectNames))
			}
			fmt.Fprintln(out, "Edit it, then run `gh todoist status` to check the resolution.")
			return nil
		},
	}
	return cmd
}

// scaffold renders the starter config. projectNames, when non-empty, is
// included as comments so the user can copy an exact name.
func scaffold(projectNames []string) string {
	var b strings.Builder

	b.WriteString("# gh-todoist configuration\n")
	b.WriteString("# Docs: https://github.com/albttx/gh-todoist\n\n")

	b.WriteString("[todoist]\n")
	b.WriteString("# The TODOIST_API_TOKEN environment variable takes precedence over this key.\n")
	b.WriteString("# api_token = \"\"\n")
	b.WriteString("# Label applied to every task this tool creates. It is also how `sync` finds\n")
	b.WriteString("# tracked tasks again, so changing it orphans existing tasks.\n")
	fmt.Fprintf(&b, "label = %q\n", config.DefaultLabel)
	b.WriteString("# Fallback project, by name. Overridden by TODOIST_PROJECT and by a\n")
	b.WriteString("# [projects.*] entry matching the current directory.\n")
	if len(projectNames) > 0 {
		fmt.Fprintf(&b, "# default_project = %q\n", projectNames[0])
	} else {
		b.WriteString("# default_project = \"Code\"\n")
	}

	if len(projectNames) > 0 {
		b.WriteString("\n# Your Todoist projects, as of `gh todoist init`:\n")
		for _, n := range projectNames {
			fmt.Fprintf(&b, "#   %s\n", n)
		}
	}

	b.WriteString("\n# Per-checkout mapping. An entry matches when the current working directory\n")
	b.WriteString("# is inside `path`; when several match, the longest path wins.\n")
	b.WriteString("# `path` supports ~ expansion.\n")
	b.WriteString("#\n")
	b.WriteString("# [projects.myrepo]\n")
	b.WriteString("# path = \"~/src/github.com/owner/repo\"\n")
	b.WriteString("# todoist_project = \"Work\"             # by name, resolved via the API\n")
	b.WriteString("# todoist_project_id = \"abc123def456\"  # by id; wins over the name if both are set\n")

	return b.String()
}
