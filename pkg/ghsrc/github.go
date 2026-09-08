package ghsrc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
)

// Issue is the subset of a GitHub issue this tool needs.
type Issue struct {
	Ref           Ref
	Title         string
	Body          string
	State         string // "open" or "closed"
	Labels        []string
	MilestoneDue  string // YYYY-MM-DD, empty when there is no milestone due date
	HTMLURL       string
	IsPullRequest bool
}

// URL returns the canonical issue URL used as the join key everywhere in the
// tool. It is always derived from the Ref rather than taken from html_url, so
// that a URL parsed back out of state.json round-trips to the same string. For
// a pull request that means the /issues/N form, which GitHub redirects to
// /pull/N anyway.
func (i Issue) URL() string { return i.Ref.IssueURL() }

// Client reads issues from GitHub. It only ever issues read requests.
type Client struct {
	rest *api.RESTClient
	gql  *api.GraphQLClient
}

// NewClient builds a client from the gh CLI's existing authentication.
func NewClient() (*Client, error) {
	rest, err := api.DefaultRESTClient()
	if err != nil {
		return nil, fmt.Errorf("github rest client (is `gh auth login` done?): %w", err)
	}
	gql, err := api.DefaultGraphQLClient()
	if err != nil {
		return nil, fmt.Errorf("github graphql client: %w", err)
	}
	return &Client{rest: rest, gql: gql}, nil
}

// CurrentRepo returns the repository of the current working directory. It
// reports ok=false rather than an error when the directory is not a checkout,
// because that is a normal condition for several commands.
func CurrentRepo() (Ref, bool) {
	r, err := repository.Current()
	if err != nil || r.Owner == "" || r.Name == "" {
		return Ref{}, false
	}
	return Ref{Owner: r.Owner, Repo: r.Name}, true
}

// restIssue mirrors the REST issue payload.
type restIssue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	Labels  []struct {
		Name string `json:"name"`
	} `json:"labels"`
	Milestone *struct {
		DueOn string `json:"due_on"`
	} `json:"milestone"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
}

func (r restIssue) toIssue(ref Ref) Issue {
	labels := make([]string, 0, len(r.Labels))
	for _, l := range r.Labels {
		labels = append(labels, l.Name)
	}
	issue := Issue{
		Ref:           ref,
		Title:         r.Title,
		Body:          r.Body,
		State:         strings.ToLower(r.State),
		Labels:        labels,
		HTMLURL:       r.HTMLURL,
		IsPullRequest: r.PullRequest != nil,
	}
	if r.Milestone != nil {
		issue.MilestoneDue = dateOnly(r.Milestone.DueOn)
	}
	return issue
}

// dateOnly trims an RFC3339 timestamp down to its YYYY-MM-DD prefix, which is
// what Todoist's due object expects.
func dateOnly(ts string) string {
	if len(ts) >= 10 && ts[4] == '-' && ts[7] == '-' {
		return ts[:10]
	}
	return ""
}

// GetIssue fetches a single issue. Pull requests are returned too: they are
// issues as far as this API is concerned, and pushing one to Todoist is valid.
func (c *Client) GetIssue(ctx context.Context, ref Ref) (Issue, error) {
	path := fmt.Sprintf("repos/%s/%s/issues/%d", ref.Owner, ref.Repo, ref.Number)
	var raw restIssue
	if err := c.rest.DoWithContext(ctx, "GET", path, nil, &raw); err != nil {
		return Issue{}, fmt.Errorf("fetch %s: %w", ref, err)
	}
	return raw.toIssue(ref), nil
}

// GetIssues fetches several issues sequentially, preserving input order.
func (c *Client) GetIssues(ctx context.Context, refs []Ref) ([]Issue, error) {
	issues := make([]Issue, 0, len(refs))
	for _, ref := range refs {
		issue, err := c.GetIssue(ctx, ref)
		if err != nil {
			return issues, err
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

// searchResponse is the payload of GET /search/issues.
type searchResponse struct {
	TotalCount int         `json:"total_count"`
	Items      []restIssue `json:"items"`
}

// SearchIssues runs a GitHub issue search and returns up to limit results.
// query is raw GitHub search syntax.
func (c *Client) SearchIssues(ctx context.Context, query string, limit int) ([]Issue, error) {
	if limit <= 0 {
		limit = 200
	}
	const perPage = 100
	var out []Issue
	for page := 1; len(out) < limit && page <= 10; page++ {
		q := url.Values{}
		q.Set("q", query)
		q.Set("per_page", strconv.Itoa(perPage))
		q.Set("page", strconv.Itoa(page))
		q.Set("sort", "updated")
		q.Set("order", "desc")

		var resp searchResponse
		path := "search/issues?" + q.Encode()
		if err := c.rest.DoWithContext(ctx, "GET", path, nil, &resp); err != nil {
			return nil, fmt.Errorf("search issues %q: %w", query, err)
		}
		for _, item := range resp.Items {
			ref, err := ParseRef(item.HTMLURL, Ref{})
			if err != nil {
				// A result we cannot address is not usable; skip rather than fail
				// the whole search.
				continue
			}
			out = append(out, item.toIssue(ref))
			if len(out) >= limit {
				break
			}
		}
		if len(resp.Items) < perPage {
			break
		}
	}
	return out, nil
}

// PartialError reports that a batched query returned usable data alongside
// per-node errors. Callers should surface it but may keep the data: the usual
// cause is a single deleted or moved issue, not a broken query.
type PartialError struct {
	Err error
}

func (e *PartialError) Error() string { return "some issues could not be resolved: " + e.Err.Error() }
func (e *PartialError) Unwrap() error { return e.Err }

// IssueState is the reconcile-relevant state of one issue on GitHub.
type IssueState struct {
	State    string // "open" or "closed"
	ClosedAt string // RFC3339, empty when open
}

// graphQLChunkSize keeps each batched query comfortably inside GitHub's node
// and complexity limits.
const graphQLChunkSize = 50

// IssueStates fetches the current state of many issues using aliased GraphQL
// nodes, chunked so each request stays small. Issues GitHub cannot resolve
// (deleted, renamed, or no longer visible) are absent from the result and
// reported through a *PartialError, which callers may log and continue past.
func (c *Client) IssueStates(ctx context.Context, refs []Ref) (map[string]IssueState, error) {
	states := make(map[string]IssueState, len(refs))
	var partial []error
	for start := 0; start < len(refs); start += graphQLChunkSize {
		end := min(start+graphQLChunkSize, len(refs))
		err := c.issueStatesChunk(ctx, refs[start:end], states)
		var pErr *PartialError
		switch {
		case err == nil:
		case errors.As(err, &pErr):
			partial = append(partial, pErr.Err)
		default:
			return states, err
		}
	}
	if len(partial) > 0 {
		return states, &PartialError{Err: errors.Join(partial...)}
	}
	return states, nil
}

func (c *Client) issueStatesChunk(ctx context.Context, refs []Ref, out map[string]IssueState) error {
	var decls, body strings.Builder
	vars := make(map[string]any, len(refs)*3)
	for i, ref := range refs {
		o, r, n := fmt.Sprintf("o%d", i), fmt.Sprintf("r%d", i), fmt.Sprintf("n%d", i)
		if i > 0 {
			decls.WriteString(", ")
		}
		fmt.Fprintf(&decls, "$%s: String!, $%s: String!, $%s: Int!", o, r, n)
		fmt.Fprintf(&body, "  i%d: repository(owner: $%s, name: $%s) { issueOrPullRequest(number: $%s) { "+
			"... on Issue { state closedAt } ... on PullRequest { state closedAt } } }\n", i, o, r, n)
		vars[o], vars[r], vars[n] = ref.Owner, ref.Repo, ref.Number
	}
	query := "query(" + decls.String() + ") {\n" + body.String() + "}"

	raw := map[string]*struct {
		IssueOrPullRequest *struct {
			State    string `json:"state"`
			ClosedAt string `json:"closedAt"`
		} `json:"issueOrPullRequest"`
	}{}

	// Partial data still arrives alongside per-node errors (a deleted issue
	// yields NOT_FOUND for its alias only), so the data is decoded either way.
	// A whole-query rejection produces the same empty result as "everything was
	// deleted", which is why it is reported rather than swallowed.
	queryErr := c.gql.DoWithContext(ctx, query, vars, &raw)
	if queryErr != nil {
		var gqlErr *api.GraphQLError
		if !errors.As(queryErr, &gqlErr) {
			return fmt.Errorf("graphql issue states: %w", queryErr)
		}
	}

	for i, ref := range refs {
		node := raw[fmt.Sprintf("i%d", i)]
		if node == nil || node.IssueOrPullRequest == nil {
			continue
		}
		out[ref.IssueURL()] = IssueState{
			State:    strings.ToLower(node.IssueOrPullRequest.State),
			ClosedAt: node.IssueOrPullRequest.ClosedAt,
		}
	}
	if queryErr != nil {
		return &PartialError{Err: queryErr}
	}
	return nil
}

// SortIssues orders issues by repository then issue number, so command output
// is stable between runs.
func SortIssues(issues []Issue) {
	sort.SliceStable(issues, func(a, b int) bool {
		ia, ib := issues[a].Ref, issues[b].Ref
		if ia.Owner != ib.Owner {
			return ia.Owner < ib.Owner
		}
		if ia.Repo != ib.Repo {
			return ia.Repo < ib.Repo
		}
		return ia.Number < ib.Number
	})
}
