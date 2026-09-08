// Package config loads gh-todoist's TOML configuration and resolves the
// effective Todoist project for a given working directory.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// DefaultLabel is applied to every task the tool creates.
const DefaultLabel = "gh"

// EnvToken is the environment variable holding the Todoist API token. It takes
// precedence over the value stored in config.toml.
const EnvToken = "TODOIST_API_TOKEN"

// EnvProject names the default Todoist project and sits between the per-repo
// mapping and [todoist].default_project in the resolution order.
const EnvProject = "TODOIST_PROJECT"

// Config is the parsed contents of config.toml.
type Config struct {
	Todoist  Todoist            `toml:"todoist"`
	Projects map[string]Project `toml:"projects"`

	// Path records where the config was loaded from. Empty if no file existed.
	Path string `toml:"-"`
}

// Todoist holds the [todoist] table.
type Todoist struct {
	APIToken       string `toml:"api_token"`
	Label          string `toml:"label"`
	DefaultProject string `toml:"default_project"`
}

// Project is a single [projects.NAME] table mapping a local checkout to a
// Todoist project.
type Project struct {
	Path             string `toml:"path"`
	TodoistProject   string `toml:"todoist_project"`
	TodoistProjectID string `toml:"todoist_project_id"`
}

// DefaultPath returns ~/.config/gh-todoist/config.toml, honouring XDG_CONFIG_HOME.
func DefaultPath() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "gh-todoist", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".config", "gh-todoist", "config.toml"), nil
}

// Load reads and parses the config file at path. A missing file yields a
// zero-value Config with defaults applied and no error, so the tool works with
// no configuration at all.
func Load(path string) (*Config, error) {
	cfg := &Config{Path: path}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		cfg.Path = ""
		cfg.applyDefaults()
		return cfg, nil
	case err != nil:
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.Path = path
	cfg.applyDefaults()
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Todoist.Label == "" {
		c.Todoist.Label = DefaultLabel
	}
}

// Token returns the Todoist API token. The TODOIST_API_TOKEN environment
// variable wins over [todoist].api_token.
func (c *Config) Token() string {
	if tok := strings.TrimSpace(os.Getenv(EnvToken)); tok != "" {
		return tok
	}
	return strings.TrimSpace(c.Todoist.APIToken)
}

// TokenSource names where Token() found the token, for diagnostics. It returns
// an empty string when no token is available. It applies the same trimming as
// Token, so status never disagrees with what the commands actually use.
func (c *Config) TokenSource() string {
	if strings.TrimSpace(os.Getenv(EnvToken)) != "" {
		return EnvToken + " env"
	}
	if strings.TrimSpace(c.Todoist.APIToken) != "" {
		return "[todoist].api_token"
	}
	return ""
}

// Label returns the label applied to every synced task.
func (c *Config) Label() string {
	if c.Todoist.Label == "" {
		return DefaultLabel
	}
	return c.Todoist.Label
}

// ExpandPath expands a leading ~ and cleans the result. It does not resolve
// symlinks; callers that need canonical paths should do so themselves.
func ExpandPath(p string) string {
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	return filepath.Clean(p)
}

// ProjectRef identifies a Todoist project either by name or by opaque id. An id
// wins over a name when both are set.
type ProjectRef struct {
	Name string
	ID   string
	// Source describes where the reference came from, for diagnostics.
	Source string
	// NameOrID marks a reference whose string came from a source that does not
	// distinguish names from ids (the --project flag). Resolvers should try a
	// name lookup first and fall back to treating it as an opaque id.
	NameOrID bool
}

// IsZero reports whether the reference selects nothing, meaning tasks land in
// the Todoist Inbox.
func (r ProjectRef) IsZero() bool { return r.Name == "" && r.ID == "" }

// String renders the reference for human-facing output.
func (r ProjectRef) String() string {
	switch {
	case r.Name != "":
		return r.Name
	case r.ID != "":
		return r.ID
	default:
		return "Inbox"
	}
}

// MatchDir returns the [projects.*] entry whose path contains dir. When several
// entries match, the longest path wins. The returned name is the table key.
func (c *Config) MatchDir(dir string) (name string, p Project, ok bool) {
	dir = filepath.Clean(dir)
	best := -1
	// Iterate a sorted key list so ties resolve deterministically.
	for _, key := range sortedKeys(c.Projects) {
		entry := c.Projects[key]
		root := ExpandPath(entry.Path)
		if root == "" || !underOrEqual(dir, root) {
			continue
		}
		if len(root) > best {
			best, name, p, ok = len(root), key, entry, true
		}
	}
	return name, p, ok
}

// underOrEqual reports whether dir is root or lives beneath it. It compares path
// segments so that /a/bc is not considered to be inside /a/b.
func underOrEqual(dir, root string) bool {
	if dir == root {
		return true
	}
	if !strings.HasSuffix(root, string(filepath.Separator)) {
		root += string(filepath.Separator)
	}
	return strings.HasPrefix(dir, root)
}

func sortedKeys(m map[string]Project) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Small maps; insertion sort keeps this dependency-free and stable.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// ResolveProject applies the documented precedence chain and returns the
// project reference to use. flag is the raw --project value, which may be a name
// or an id; dir is the current working directory.
//
// Order: --project flag, [projects.*] entry matching dir, TODOIST_PROJECT env,
// [todoist].default_project, then Inbox.
func (c *Config) ResolveProject(flag, dir string) ProjectRef {
	if flag = strings.TrimSpace(flag); flag != "" {
		// A flag value is ambiguous between name and id. Treat it as a name and
		// let the client fall back to an id lookup when no name matches.
		return ProjectRef{Name: flag, Source: "--project flag", NameOrID: true}
	}
	if name, p, ok := c.MatchDir(dir); ok {
		if p.TodoistProjectID != "" {
			return ProjectRef{ID: p.TodoistProjectID, Source: "[projects." + name + "].todoist_project_id"}
		}
		if p.TodoistProject != "" {
			return ProjectRef{Name: p.TodoistProject, Source: "[projects." + name + "].todoist_project"}
		}
	}
	if env := strings.TrimSpace(os.Getenv(EnvProject)); env != "" {
		return ProjectRef{Name: env, Source: EnvProject + " env"}
	}
	if def := strings.TrimSpace(c.Todoist.DefaultProject); def != "" {
		return ProjectRef{Name: def, Source: "[todoist].default_project"}
	}
	return ProjectRef{Source: "default (Inbox)"}
}
