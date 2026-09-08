package ghsrc

import "testing"

func TestParseRef(t *testing.T) {
	t.Parallel()

	cwdRepo := Ref{Owner: "acme", Repo: "widgets"}

	tests := []struct {
		name     string
		input    string
		fallback Ref
		want     Ref
		wantErr  bool
	}{
		{
			name:     "bare number uses the current repo",
			input:    "812",
			fallback: cwdRepo,
			want:     Ref{Owner: "acme", Repo: "widgets", Number: 812},
		},
		{
			name:     "leading hash on a bare number",
			input:    "#812",
			fallback: cwdRepo,
			want:     Ref{Owner: "acme", Repo: "widgets", Number: 812},
		},
		{
			name:  "qualified form",
			input: "acme-corp/ansible#44",
			want:  Ref{Owner: "acme-corp", Repo: "ansible", Number: 44},
		},
		{
			name:  "qualified form ignores the fallback",
			input: "other/repo#7",
			// fallback deliberately set: an explicit repo must win.
			fallback: cwdRepo,
			want:     Ref{Owner: "other", Repo: "repo", Number: 7},
		},
		{
			name:  "full issue url",
			input: "https://github.com/acme/widgets/issues/812",
			want:  Ref{Owner: "acme", Repo: "widgets", Number: 812},
		},
		{
			name:  "pull request url is accepted",
			input: "https://github.com/cli/cli/pull/9001",
			want:  Ref{Owner: "cli", Repo: "cli", Number: 9001},
		},
		{
			name:  "url with a fragment and query",
			input: "https://github.com/o/r/issues/5?foo=bar#issuecomment-1",
			want:  Ref{Owner: "o", Repo: "r", Number: 5},
		},
		{
			name:  "schemeless github url",
			input: "github.com/o/r/issues/5",
			want:  Ref{Owner: "o", Repo: "r", Number: 5},
		},
		{
			name:  "repo names with dots and dashes",
			input: "acme/widgets.dev#3",
			want:  Ref{Owner: "acme", Repo: "widgets.dev", Number: 3},
		},
		{
			name:     "surrounding whitespace tolerated",
			input:    "  812  ",
			fallback: cwdRepo,
			want:     Ref{Owner: "acme", Repo: "widgets", Number: 812},
		},
		{
			name:    "bare number without a current repo",
			input:   "812",
			wantErr: true,
		},
		{name: "empty", input: "", wantErr: true},
		{name: "zero issue number", input: "owner/repo#0", wantErr: true},
		{name: "not a number", input: "owner/repo#abc", wantErr: true},
		{name: "missing repo", input: "owner#5", wantErr: true},
		{name: "url that is not an issue", input: "https://github.com/o/r", wantErr: true},
		{name: "non-github noise", input: "just some text", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseRef(tt.input, tt.fallback)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseRef(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Errorf("ParseRef(%q) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}

func TestRefRendering(t *testing.T) {
	t.Parallel()
	ref := Ref{Owner: "acme", Repo: "widgets", Number: 812}
	if got := ref.String(); got != "acme/widgets#812" {
		t.Errorf("String() = %q", got)
	}
	if got := ref.IssueURL(); got != "https://github.com/acme/widgets/issues/812" {
		t.Errorf("IssueURL() = %q", got)
	}
}

// TestRefRoundTrip guards the join key. state.json is keyed by IssueURL, and
// sync parses those keys back into refs to query GitHub. If the round trip were
// lossy, sync would silently fail to reconcile whatever it could not re-derive.
func TestRefRoundTrip(t *testing.T) {
	t.Parallel()
	for _, ref := range []Ref{
		{Owner: "acme", Repo: "widgets", Number: 812},
		{Owner: "cli", Repo: "cli", Number: 1},
		{Owner: "acme", Repo: "widgets.dev", Number: 42},
	} {
		back, err := ParseRef(ref.IssueURL(), Ref{})
		if err != nil {
			t.Fatalf("ParseRef(%q) error = %v", ref.IssueURL(), err)
		}
		if back != ref {
			t.Errorf("round trip changed %+v into %+v", ref, back)
		}
	}
}

// TestPullRequestURLCanonicalises documents why Issue.URL derives the URL from
// the ref rather than taking GitHub's html_url: a pull request's html_url uses
// /pull/, which would not round trip against the /issues/ form used everywhere.
func TestPullRequestURLCanonicalises(t *testing.T) {
	t.Parallel()
	ref, err := ParseRef("https://github.com/cli/cli/pull/9001", Ref{})
	if err != nil {
		t.Fatalf("ParseRef error = %v", err)
	}
	issue := Issue{Ref: ref, HTMLURL: "https://github.com/cli/cli/pull/9001"}
	if got := issue.URL(); got != "https://github.com/cli/cli/issues/9001" {
		t.Errorf("URL() = %q, want the canonical /issues/ form", got)
	}
	back, err := ParseRef(issue.URL(), Ref{})
	if err != nil || back != ref {
		t.Errorf("canonical URL does not round trip: %+v, %v", back, err)
	}
}

func TestParseRefsCollectsAllErrors(t *testing.T) {
	t.Parallel()
	refs, err := ParseRefs([]string{"o/r#1", "nonsense", "o/r#2", "also bad"}, Ref{})
	if err == nil {
		t.Fatal("expected an error naming the bad arguments")
	}
	if len(refs) != 2 {
		t.Errorf("parsed %d refs, want the 2 good ones", len(refs))
	}
}

func TestSortIssues(t *testing.T) {
	t.Parallel()
	issues := []Issue{
		{Ref: Ref{Owner: "b", Repo: "x", Number: 2}},
		{Ref: Ref{Owner: "a", Repo: "z", Number: 1}},
		{Ref: Ref{Owner: "a", Repo: "z", Number: 10}},
		{Ref: Ref{Owner: "a", Repo: "a", Number: 5}},
	}
	SortIssues(issues)
	want := []string{"a/a#5", "a/z#1", "a/z#10", "b/x#2"}
	for i, w := range want {
		if got := issues[i].Ref.String(); got != w {
			t.Errorf("issues[%d] = %s, want %s", i, got, w)
		}
	}
}

func TestDateOnly(t *testing.T) {
	t.Parallel()
	tests := []struct{ in, want string }{
		{in: "2026-10-01T07:00:00Z", want: "2026-10-01"},
		{in: "2026-10-01", want: "2026-10-01"},
		{in: "", want: ""},
		{in: "garbage", want: ""},
	}
	for _, tt := range tests {
		if got := dateOnly(tt.in); got != tt.want {
			t.Errorf("dateOnly(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
