package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfig drops a config.toml into a temp dir and loads it.
func writeConfig(t *testing.T, body string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return cfg
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil for a missing file", err)
	}
	if cfg.Label() != DefaultLabel {
		t.Errorf("Label() = %q, want the default", cfg.Label())
	}
	if cfg.Path != "" {
		t.Errorf("Path = %q, want empty when no file exists", cfg.Path)
	}
}

func TestLoadParsesTheDocumentedSchema(t *testing.T) {
	cfg := writeConfig(t, `
[todoist]
api_token = "from-file"
label = "todo"
default_project = "Chores 🧹"

[projects.widgets]
path = "~/src/github.com/acme/widgets"
todoist_project = "Work"

[projects.alpha]
path = "/work/alpha"
todoist_project_id = "abc123def456"
`)

	if cfg.Todoist.APIToken != "from-file" {
		t.Errorf("APIToken = %q", cfg.Todoist.APIToken)
	}
	if cfg.Label() != "todo" {
		t.Errorf("Label() = %q", cfg.Label())
	}
	if cfg.Todoist.DefaultProject != "Chores 🧹" {
		t.Errorf("DefaultProject = %q", cfg.Todoist.DefaultProject)
	}
	if len(cfg.Projects) != 2 {
		t.Fatalf("got %d project entries, want 2", len(cfg.Projects))
	}
	if cfg.Projects["widgets"].TodoistProject != "Work" {
		t.Errorf("widgets mapping = %#v", cfg.Projects["widgets"])
	}
	if cfg.Projects["alpha"].TodoistProjectID != "abc123def456" {
		t.Errorf("alpha mapping = %#v", cfg.Projects["alpha"])
	}
}

func TestTokenPrecedence(t *testing.T) {
	cfg := writeConfig(t, "[todoist]\napi_token = \"from-file\"\n")

	t.Run("file token when env is unset", func(t *testing.T) {
		t.Setenv(EnvToken, "")
		if got := cfg.Token(); got != "from-file" {
			t.Errorf("Token() = %q, want the file value", got)
		}
	})

	t.Run("env wins over file", func(t *testing.T) {
		t.Setenv(EnvToken, "from-env")
		if got := cfg.Token(); got != "from-env" {
			t.Errorf("Token() = %q, want the environment value", got)
		}
	})

	t.Run("whitespace-only env does not win", func(t *testing.T) {
		t.Setenv(EnvToken, "   ")
		if got := cfg.Token(); got != "from-file" {
			t.Errorf("Token() = %q, want the file value", got)
		}
	})
}

func TestTokenSourceAgreesWithToken(t *testing.T) {
	cfg := writeConfig(t, "[todoist]\napi_token = \"from-file\"\n")
	empty := writeConfig(t, "[todoist]\nlabel = \"gh\"\n")

	tests := []struct {
		name       string
		cfg        *Config
		env        string
		wantSource string
		wantToken  string
	}{
		{name: "env", cfg: cfg, env: "e", wantSource: EnvToken + " env", wantToken: "e"},
		{name: "file", cfg: cfg, env: "", wantSource: "[todoist].api_token", wantToken: "from-file"},
		{name: "whitespace env falls through to the file", cfg: cfg, env: "  ", wantSource: "[todoist].api_token", wantToken: "from-file"},
		{name: "none", cfg: empty, env: "", wantSource: "", wantToken: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvToken, tt.env)
			if got := tt.cfg.TokenSource(); got != tt.wantSource {
				t.Errorf("TokenSource() = %q, want %q", got, tt.wantSource)
			}
			// status reports TokenSource while the commands use Token; they must
			// never disagree about whether a token exists.
			if (tt.cfg.TokenSource() == "") != (tt.cfg.Token() == "") {
				t.Error("TokenSource and Token disagree about token availability")
			}
			if got := tt.cfg.Token(); got != tt.wantToken {
				t.Errorf("Token() = %q, want %q", got, tt.wantToken)
			}
		})
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory available")
	}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "absolute untouched", in: "/work/alpha", want: "/work/alpha"},
		{name: "tilde alone", in: "~", want: home},
		{name: "tilde prefix", in: "~/go/src", want: filepath.Join(home, "go/src")},
		{name: "cleaned", in: "/a/b/../c", want: "/a/c"},
		{name: "tilde not at the start is literal", in: "/a/~b", want: "/a/~b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExpandPath(tt.in); got != tt.want {
				t.Errorf("ExpandPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMatchDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory available")
	}

	cfg := writeConfig(t, `
[projects.outer]
path = "/work"
todoist_project = "Outer"

[projects.inner]
path = "/work/alpha"
todoist_project = "Inner"

[projects.tilde]
path = "~/src/github.com/acme/widgets"
todoist_project = "Work"

[projects.lookalike]
path = "/work/al"
todoist_project = "Lookalike"
`)

	tests := []struct {
		name    string
		dir     string
		wantKey string
		wantOK  bool
	}{
		{name: "exact match on the root", dir: "/work", wantKey: "outer", wantOK: true},
		{name: "nested picks the shallow entry", dir: "/work/other/pkg", wantKey: "outer", wantOK: true},
		{name: "longest path wins", dir: "/work/alpha", wantKey: "inner", wantOK: true},
		{name: "deep inside the longest match", dir: "/work/alpha/internal/auth", wantKey: "inner", wantOK: true},
		{name: "sibling prefix is not a match", dir: "/work/alphaX", wantKey: "outer", wantOK: true},
		{name: "segment-aware, /work/al is its own entry", dir: "/work/al", wantKey: "lookalike", wantOK: true},
		{name: "tilde entry expands", dir: filepath.Join(home, "src/github.com/acme/widgets/pkg"), wantKey: "tilde", wantOK: true},
		{name: "unrelated directory", dir: "/elsewhere", wantOK: false},
		{name: "trailing slash tolerated", dir: "/work/alpha/", wantKey: "inner", wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, _, ok := cfg.MatchDir(tt.dir)
			if ok != tt.wantOK {
				t.Fatalf("MatchDir(%q) ok = %v, want %v (matched %q)", tt.dir, ok, tt.wantOK, key)
			}
			if ok && key != tt.wantKey {
				t.Errorf("MatchDir(%q) = %q, want %q", tt.dir, key, tt.wantKey)
			}
		})
	}
}

func TestResolveProjectPrecedence(t *testing.T) {
	cfg := writeConfig(t, `
[todoist]
default_project = "Fallback"

[projects.alpha]
path = "/work/alpha"
todoist_project = "Mapped"
`)

	tests := []struct {
		name      string
		flag      string
		dir       string
		env       string
		wantName  string
		wantID    string
		wantIsSet bool
	}{
		{
			name:      "flag beats everything",
			flag:      "Explicit",
			dir:       "/work/alpha",
			env:       "FromEnv",
			wantName:  "Explicit",
			wantIsSet: true,
		},
		{
			name:      "cwd mapping beats env and default",
			dir:       "/work/alpha/internal",
			env:       "FromEnv",
			wantName:  "Mapped",
			wantIsSet: true,
		},
		{
			name:      "env beats the default when cwd does not match",
			dir:       "/elsewhere",
			env:       "FromEnv",
			wantName:  "FromEnv",
			wantIsSet: true,
		},
		{
			name:      "default project last",
			dir:       "/elsewhere",
			wantName:  "Fallback",
			wantIsSet: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvProject, tt.env)
			got := cfg.ResolveProject(tt.flag, tt.dir)
			if got.Name != tt.wantName {
				t.Errorf("Name = %q, want %q (source %q)", got.Name, tt.wantName, got.Source)
			}
			if got.IsZero() == tt.wantIsSet {
				t.Errorf("IsZero() = %v, want %v", got.IsZero(), !tt.wantIsSet)
			}
		})
	}
}

func TestResolveProjectFallsBackToInbox(t *testing.T) {
	t.Setenv(EnvProject, "")
	cfg := writeConfig(t, "[todoist]\nlabel = \"gh\"\n")
	got := cfg.ResolveProject("", "/elsewhere")
	if !got.IsZero() {
		t.Errorf("expected the Inbox fallback, got %#v", got)
	}
	if got.String() != "Inbox" {
		t.Errorf("String() = %q, want Inbox", got.String())
	}
}

func TestResolveProjectIDBeatsNameInSameEntry(t *testing.T) {
	t.Setenv(EnvProject, "")
	cfg := writeConfig(t, `
[projects.alpha]
path = "/work/alpha"
todoist_project = "By Name"
todoist_project_id = "abc123def456"
`)
	got := cfg.ResolveProject("", "/work/alpha")
	if got.ID != "abc123def456" {
		t.Errorf("ID = %q, want the configured id to win over the name", got.ID)
	}
	if got.Name != "" {
		t.Errorf("Name = %q, want empty when the id wins", got.Name)
	}
}

func TestResolveProjectFlagIsAmbiguous(t *testing.T) {
	t.Setenv(EnvProject, "")
	cfg := writeConfig(t, "")
	got := cfg.ResolveProject("proj0000000001", "/tmp")
	if !got.NameOrID {
		t.Error("a --project value must be marked as possibly-an-id so lookup can fall back")
	}
}

// TestConfirmPick covers the three states a defaulted-true boolean has to keep
// apart. A plain bool field would collapse the first two, silently disabling a
// confirmation the user never asked to lose.
func TestConfirmPick(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "no [pick] table at all",
			body: "[todoist]\nlabel = \"gh\"\n",
			want: true,
		},
		{
			name: "[pick] present but empty",
			body: "[pick]\n",
			want: true,
		},
		{
			name: "explicitly on",
			body: "[pick]\nconfirm = true\n",
			want: true,
		},
		{
			name: "explicitly off",
			body: "[pick]\nconfirm = false\n",
			want: false,
		},
		{
			name: "empty file",
			body: "",
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := writeConfig(t, tt.body)
			if got := cfg.ConfirmPick(); got != tt.want {
				t.Errorf("ConfirmPick() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfirmPickDefaultsOnWithNoConfigFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.ConfirmPick() {
		t.Error("ConfirmPick() = false with no config file, want true")
	}
}

// TestConfirmPickOffIsNotAParseArtifact guards the distinction directly: the
// decoded pointer must be non-nil only when the key was actually written.
func TestConfirmPickOffIsNotAParseArtifact(t *testing.T) {
	if cfg := writeConfig(t, "[todoist]\nlabel = \"gh\"\n"); cfg.Pick.Confirm != nil {
		t.Errorf("Pick.Confirm = %v, want nil when the key is absent", *cfg.Pick.Confirm)
	}
	cfg := writeConfig(t, "[pick]\nconfirm = false\n")
	if cfg.Pick.Confirm == nil {
		t.Fatal("Pick.Confirm = nil, want a decoded false")
	}
	if *cfg.Pick.Confirm {
		t.Error("Pick.Confirm = true, want false")
	}
}
