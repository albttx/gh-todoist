// Package cmd wires the cobra command tree for gh-todoist.
package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/albttx/gh-todoist/internal/config"
	"github.com/albttx/gh-todoist/internal/state"
	"github.com/albttx/gh-todoist/pkg/ghsrc"
	"github.com/albttx/gh-todoist/pkg/todoist"
)

// app carries everything the subcommands share. It is built lazily so that
// commands which do not need network access (or a token) still run.
type app struct {
	cfg    *config.Config
	st     *state.State
	out    io.Writer
	errOut io.Writer
	cwd    string

	todoistBaseURL string

	td *todoist.Client
	gh *ghsrc.Client
}

// newApp loads configuration and state. It does not contact any API.
func newApp(out, errOut io.Writer) (*app, error) {
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	statePath, err := state.DefaultPath()
	if err != nil {
		return nil, err
	}
	st, err := state.Load(statePath)
	if err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("determine working directory: %w", err)
	}

	base := todoist.DefaultBaseURL
	if override := strings.TrimSpace(os.Getenv("TODOIST_API_BASE_URL")); override != "" {
		base = override
	}

	return &app{
		cfg:            cfg,
		st:             st,
		out:            out,
		errOut:         errOut,
		cwd:            cwd,
		todoistBaseURL: base,
	}, nil
}

// todoistClient returns the Todoist client, failing loudly when no token is
// configured, since that is the single most common setup mistake.
func (a *app) todoistClient() (*todoist.Client, error) {
	if a.td != nil {
		return a.td, nil
	}
	token := a.cfg.Token()
	if token == "" {
		return nil, fmt.Errorf("no Todoist API token: set %s or add api_token to the [todoist] table of %s",
			config.EnvToken, a.configPathForMessage())
	}
	a.td = todoist.New(a.todoistBaseURL, token)
	return a.td, nil
}

func (a *app) githubClient() (*ghsrc.Client, error) {
	if a.gh != nil {
		return a.gh, nil
	}
	c, err := ghsrc.NewClient()
	if err != nil {
		return nil, err
	}
	a.gh = c
	return a.gh, nil
}

// configPathForMessage names the config file even when none exists yet.
func (a *app) configPathForMessage() string {
	if a.cfg.Path != "" {
		return a.cfg.Path
	}
	if p, err := config.DefaultPath(); err == nil {
		return p + " (not created yet, run `gh todoist init`)"
	}
	return "config.toml"
}

// resolvedProject is a project reference turned into something the API accepts.
type resolvedProject struct {
	// ID is empty when tasks should go to the Inbox.
	ID string
	// Display is what to show the user.
	Display string
	// Source explains which configuration layer chose this project.
	Source string
}

// resolveProject turns a ProjectRef into a project id. Names are looked up in
// the state cache first and the cache is refreshed when a name misses, because
// a project created since the last run would otherwise fail forever.
//
// A refresh writes state.json. That is the one local write `status` can make,
// and its help text says so.
func (a *app) resolveProject(ctx context.Context, ref config.ProjectRef) (resolvedProject, error) {
	if ref.IsZero() {
		return resolvedProject{Display: "Inbox", Source: ref.Source}, nil
	}
	if ref.ID != "" {
		return resolvedProject{ID: ref.ID, Display: ref.ID, Source: ref.Source}, nil
	}

	if id, ok := a.st.CachedProjectID(ref.Name); ok {
		return resolvedProject{ID: id, Display: ref.Name, Source: ref.Source}, nil
	}

	client, err := a.todoistClient()
	if err != nil {
		return resolvedProject{}, err
	}
	projects, err := client.ListProjects(ctx)
	if err != nil {
		return resolvedProject{}, fmt.Errorf("resolve project %q: %w", ref.Name, err)
	}
	byName := todoist.ProjectsByName(projects)
	a.st.CacheProjects(byName, time.Now().UTC())
	if err := a.st.Save(); err != nil {
		// A cache write failure is not worth aborting the command over.
		fmt.Fprintf(a.errOut, "warning: could not cache project list: %v\n", err)
	}

	if id, ok := byName[ref.Name]; ok {
		return resolvedProject{ID: id, Display: ref.Name, Source: ref.Source}, nil
	}

	// --project does not distinguish a name from an id, so fall back to
	// treating the value as an opaque project id if one matches.
	if ref.NameOrID {
		for _, p := range projects {
			if p.ID == ref.Name {
				return resolvedProject{ID: p.ID, Display: p.Name, Source: ref.Source}, nil
			}
		}
	}

	names := todoist.ProjectNames(projects)
	sort.Strings(names)
	return resolvedProject{}, fmt.Errorf(
		"project %q not found in Todoist (from %s)\navailable projects:\n  %s",
		ref.Name, ref.Source, strings.Join(names, "\n  "))
}

// trackedRefs converts the state's issue URLs back into refs, dropping any that
// no longer parse. The returned slice is sorted for deterministic output.
func trackedRefs(st *state.State, includeTombstoned bool) []ghsrc.Ref {
	urls := make([]string, 0, len(st.Issues))
	for u, rec := range st.Issues {
		if rec.Tombstoned && !includeTombstoned {
			continue
		}
		urls = append(urls, u)
	}
	sort.Strings(urls)

	refs := make([]ghsrc.Ref, 0, len(urls))
	for _, u := range urls {
		ref, err := ghsrc.ParseRef(u, ghsrc.Ref{})
		if err != nil {
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}
