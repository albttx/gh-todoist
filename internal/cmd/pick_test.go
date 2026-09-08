package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestBuildSearchQuery covers the full pool matrix: inside a repository versus
// outside one, crossed with the flag being absent, bare, or given a value.
//
// The rule being pinned is that inside a repository the default pool is every
// open issue, while outside one it narrows to your own — an unfiltered pool
// there would be every open issue on GitHub, which is not curatable.
func TestBuildSearchQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rawQuery  string
		repo      string
		assign    string
		wantQuery string
		wantScope string
	}{
		// --- in a repository ---
		{
			name:      "in repo, no flag: every open issue",
			repo:      "acme/widgets",
			wantQuery: "is:open is:issue repo:acme/widgets",
			wantScope: "open issues in acme/widgets",
		},
		{
			name:      "in repo, bare --assign: yourself",
			repo:      "acme/widgets",
			assign:    "me", // what NoOptDefVal supplies for the bare flag
			wantQuery: "is:open is:issue repo:acme/widgets assignee:@me",
			wantScope: "open issues in acme/widgets assigned to you",
		},
		{
			name:      "in repo, --assign alice",
			repo:      "acme/widgets",
			assign:    "alice",
			wantQuery: "is:open is:issue repo:acme/widgets assignee:alice",
			wantScope: "open issues in acme/widgets assigned to alice",
		},

		// --- outside a repository ---
		{
			name:      "no repo, no flag: falls back to yourself",
			assign:    "",
			wantQuery: "is:open is:issue assignee:@me",
			wantScope: "open issues assigned to you",
		},
		{
			name:      "no repo, bare --assign: same as the fallback",
			assign:    "me",
			wantQuery: "is:open is:issue assignee:@me",
			wantScope: "open issues assigned to you",
		},
		{
			name:      "no repo, --assign alice",
			assign:    "alice",
			wantQuery: "is:open is:issue assignee:alice",
			wantScope: "open issues assigned to alice",
		},

		// --- normalisation and overrides ---
		{
			name:      "explicit @me is accepted",
			repo:      "acme/widgets",
			assign:    "@me",
			wantQuery: "is:open is:issue repo:acme/widgets assignee:@me",
		},
		{
			name:      "a leading @ on a username is stripped",
			repo:      "acme/widgets",
			assign:    "@alice",
			wantQuery: "is:open is:issue repo:acme/widgets assignee:alice",
		},
		{
			name:      "whitespace around the assignee is ignored",
			repo:      "acme/widgets",
			assign:    "  alice  ",
			wantQuery: "is:open is:issue repo:acme/widgets assignee:alice",
		},
		{
			name:      "raw query replaces the pool entirely",
			rawQuery:  "is:open label:bug org:acme",
			repo:      "acme/widgets",
			assign:    "alice",
			wantQuery: "is:open label:bug org:acme",
		},
		{
			name:      "raw query is trimmed",
			rawQuery:  "  is:open author:@me  ",
			wantQuery: "is:open author:@me",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, scope := buildSearchQuery(tt.rawQuery, tt.repo, tt.assign)
			if got != tt.wantQuery {
				t.Errorf("query = %q, want %q", got, tt.wantQuery)
			}
			if tt.wantScope != "" && scope != tt.wantScope {
				t.Errorf("scope = %q, want %q", scope, tt.wantScope)
			}
			if scope == "" {
				t.Error("scope description must not be empty")
			}
		})
	}
}

// TestBuildSearchQueryNeverLeavesThePoolUnbounded guards the one combination
// that must not exist: no repository and no assignee, which would ask GitHub
// for every open issue in the world.
func TestBuildSearchQueryNeverLeavesThePoolUnbounded(t *testing.T) {
	t.Parallel()
	got, _ := buildSearchQuery("", "", "")
	if !strings.Contains(got, "assignee:") {
		t.Errorf("query = %q, want an assignee filter when there is no repository", got)
	}
}

func TestNormalizeAssignee(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{in: "", want: ""},
		{in: "   ", want: ""},
		{in: "me", want: "@me"},
		{in: "@me", want: "@me"},
		{in: "alice", want: "alice"},
		{in: "@alice", want: "alice"},
		{in: " alice ", want: "alice"},
	}
	for _, tt := range tests {
		if got := normalizeAssignee(tt.in); got != tt.want {
			t.Errorf("normalizeAssignee(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestPickArgs covers the pflag quirk this command has to absorb: a flag with
// NoOptDefVal cannot take a space-separated value, so `--assign alice` reaches
// cobra as a bare flag plus a stray argument.
func TestPickArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		argv       []string
		wantErr    bool
		wantErrHas string
	}{
		{name: "no arguments", argv: []string{}},
		{name: "assign with a separated value", argv: []string{"--assign", "alice"}},
		{name: "bare assign", argv: []string{"--assign"}},
		{name: "assign with an attached value", argv: []string{"--assign=alice"}},
		{
			name:       "stray argument without the flag",
			argv:       []string{"nonsense"},
			wantErr:    true,
			wantErrHas: "takes no arguments",
		},
		{
			name:       "two stray arguments",
			argv:       []string{"--assign", "alice", "bob"},
			wantErr:    true,
			wantErrHas: "takes no arguments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := newPickCmd(&bytes.Buffer{}, &bytes.Buffer{})
			if err := cmd.ParseFlags(tt.argv); err != nil {
				t.Fatalf("ParseFlags(%v) error = %v", tt.argv, err)
			}
			err := pickArgs(cmd, cmd.Flags().Args())
			if (err != nil) != tt.wantErr {
				t.Fatalf("pickArgs(%v) error = %v, wantErr %v", tt.argv, err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), tt.wantErrHas) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantErrHas)
			}
		})
	}
}

// TestPickAssignFlagHasNoOptDefVal pins the mechanism the bare form depends on.
func TestPickAssignFlagHasNoOptDefVal(t *testing.T) {
	t.Parallel()
	cmd := newPickCmd(&bytes.Buffer{}, &bytes.Buffer{})
	f := cmd.Flags().Lookup("assign")
	if f == nil {
		t.Fatal("pick has no --assign flag")
	}
	if f.NoOptDefVal != "me" {
		t.Errorf("NoOptDefVal = %q, want \"me\" so that a bare --assign works", f.NoOptDefVal)
	}
	if f.DefValue != "" {
		t.Errorf("DefValue = %q, want empty so an absent flag is distinguishable", f.DefValue)
	}
}
