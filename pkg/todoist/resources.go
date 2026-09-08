package todoist

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// Project is a Todoist project. Ids are opaque strings.
type Project struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	IsBase bool   `json:"is_inbox_project"`
}

// Task is a Todoist task as returned by the listing endpoints.
type Task struct {
	ID        string   `json:"id"`
	Content   string   `json:"content"`
	ProjectID string   `json:"project_id"`
	Labels    []string `json:"labels"`
	Priority  int      `json:"priority"`
}

// page is the shape of every cursor-paginated list response.
type page[T any] struct {
	Results    []T     `json:"results"`
	NextCursor *string `json:"next_cursor"`
}

// ListProjects returns every project, following cursor pagination.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	return listAll[Project](ctx, c, "/projects", nil)
}

// ListTasksByLabel returns the open tasks carrying label. Todoist warns that
// concurrent edits can duplicate or skip items across pages, so results are
// deduplicated by task id.
func (c *Client) ListTasksByLabel(ctx context.Context, label string) ([]Task, error) {
	q := url.Values{}
	q.Set("label", label)
	tasks, err := listAll[Task](ctx, c, "/tasks", q)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(tasks))
	out := tasks[:0]
	for _, t := range tasks {
		if _, dup := seen[t.ID]; dup {
			continue
		}
		seen[t.ID] = struct{}{}
		out = append(out, t)
	}
	return out, nil
}

// listAll walks every page of a cursor-paginated collection endpoint.
func listAll[T any](ctx context.Context, c *Client, path string, base url.Values) ([]T, error) {
	var all []T
	cursor := ""
	// Guard against a server that keeps handing back the same cursor.
	for i := 0; i < 1000; i++ {
		q := url.Values{}
		for k, v := range base {
			q[k] = v
		}
		q.Set("limit", strconv.Itoa(MaxPageLimit))
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var p page[T]
		if err := c.do(ctx, "GET", path, q, nil, "", &p); err != nil {
			return nil, fmt.Errorf("list %s: %w", path, err)
		}
		all = append(all, p.Results...)
		if p.NextCursor == nil || *p.NextCursor == "" || *p.NextCursor == cursor {
			return all, nil
		}
		cursor = *p.NextCursor
	}
	return all, fmt.Errorf("list %s: pagination did not terminate", path)
}

// ProjectsByName builds an exact-name to id map. Duplicate names keep the first
// project seen, which matches Todoist's own ordering.
func ProjectsByName(projects []Project) map[string]string {
	byName := make(map[string]string, len(projects))
	for _, p := range projects {
		if _, ok := byName[p.Name]; !ok {
			byName[p.Name] = p.ID
		}
	}
	return byName
}

// ProjectNames returns the project names in API order, for error messages.
func ProjectNames(projects []Project) []string {
	names := make([]string, 0, len(projects))
	for _, p := range projects {
		names = append(names, p.Name)
	}
	return names
}
