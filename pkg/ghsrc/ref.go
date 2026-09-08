// Package ghsrc reads issues from GitHub.
//
// It reuses the gh CLI's stored credentials via cli/go-gh, so a caller needs no
// token of its own, and it is strictly read-only: every method issues GET
// requests or read-only GraphQL queries. Nothing in this package can close an
// issue, comment, label, or assign.
//
// # References
//
// [ParseRef] accepts the three forms a user is likely to type — a bare number
// resolved against a fallback repository, "owner/repo#812", and a full issue or
// pull request URL:
//
//	current, _ := ghsrc.CurrentRepo()
//	ref, err := ghsrc.ParseRef("812", current)
//
// [Ref.IssueURL] is the canonical identity of an issue and round-trips through
// [ParseRef]. Note that [Issue.URL] derives from the ref rather than echoing
// GitHub's html_url: a pull request's html_url uses /pull/N, which would not
// round-trip against the /issues/N form. Use it as a join key, not html_url.
//
// # Batched state lookups
//
// [Client.IssueStates] answers "is this still open?" for many issues using
// aliased GraphQL nodes, chunked per request, rather than N REST calls. Issues
// GitHub cannot resolve — deleted, moved, or no longer visible — are absent
// from the result and reported through a [PartialError] that the caller may log
// and continue past, since the accompanying data is still usable.
package ghsrc

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Ref identifies one GitHub issue.
type Ref struct {
	Owner  string
	Repo   string
	Number int
}

// String renders the ref as owner/repo#number.
func (r Ref) String() string { return fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number) }

// IssueURL returns the canonical github.com URL, which is the join key used
// throughout the tool.
func (r Ref) IssueURL() string {
	return fmt.Sprintf("https://github.com/%s/%s/issues/%d", r.Owner, r.Repo, r.Number)
}

var (
	// owner/repo#123
	qualifiedRe = regexp.MustCompile(`^([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)#(\d+)$`)
	// bare issue number
	bareRe = regexp.MustCompile(`^#?(\d+)$`)
	// .../owner/repo/issues/123 or .../owner/repo/pull/123
	urlPathRe = regexp.MustCompile(`^/([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)/(?:issues|pull)/(\d+)`)
)

// ParseRef resolves an issue reference. Accepted forms are a bare number
// (which takes owner and repo from fallback, normally the current checkout),
// "owner/repo#123", and a full issue or pull request URL.
func ParseRef(s string, fallback Ref) (Ref, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Ref{}, fmt.Errorf("empty issue reference")
	}

	if m := qualifiedRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[3])
		if err != nil || n <= 0 {
			return Ref{}, fmt.Errorf("invalid issue number in %q", s)
		}
		return Ref{Owner: m[1], Repo: m[2], Number: n}, nil
	}

	if m := bareRe.FindStringSubmatch(s); m != nil {
		if fallback.Owner == "" || fallback.Repo == "" {
			return Ref{}, fmt.Errorf("cannot resolve %q: not inside a GitHub repository, use owner/repo#%s", s, m[1])
		}
		n, err := strconv.Atoi(m[1])
		if err != nil || n <= 0 {
			return Ref{}, fmt.Errorf("invalid issue number in %q", s)
		}
		return Ref{Owner: fallback.Owner, Repo: fallback.Repo, Number: n}, nil
	}

	if strings.Contains(s, "://") || strings.HasPrefix(s, "github.com/") {
		raw := s
		if !strings.Contains(raw, "://") {
			raw = "https://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil {
			return Ref{}, fmt.Errorf("parse issue url %q: %w", s, err)
		}
		m := urlPathRe.FindStringSubmatch(u.Path)
		if m == nil {
			return Ref{}, fmt.Errorf("not a GitHub issue url: %q", s)
		}
		n, err := strconv.Atoi(m[3])
		if err != nil || n <= 0 {
			return Ref{}, fmt.Errorf("invalid issue number in %q", s)
		}
		return Ref{Owner: m[1], Repo: m[2], Number: n}, nil
	}

	return Ref{}, fmt.Errorf("unrecognised issue reference %q: want 812, owner/repo#812, or a full issue URL", s)
}

// ParseRefs resolves a list of references, collecting all errors so the user
// sees every bad argument at once rather than one per run.
func ParseRefs(args []string, fallback Ref) ([]Ref, error) {
	refs := make([]Ref, 0, len(args))
	var problems []string
	for _, a := range args {
		r, err := ParseRef(a, fallback)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		refs = append(refs, r)
	}
	if len(problems) > 0 {
		return refs, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return refs, nil
}
