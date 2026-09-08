package todoist

import (
	"fmt"
	"strings"
)

// DescriptionLimit is how much of a GitHub issue body is carried across. The
// task description is a reminder, not a mirror.
const DescriptionLimit = 400

// IssueInput is the GitHub-side data needed to build a task, expressed without
// depending on the GitHub client so this stays trivially testable.
type IssueInput struct {
	// URL is the canonical issue URL and the idempotency key input.
	URL string
	// Ref is the human label, e.g. "owner/repo#812".
	Ref string
	// Title is the issue title.
	Title string
	// Body is the raw issue body markdown.
	Body string
	// Labels are the GitHub label names, used for the priority mapping.
	Labels []string
	// MilestoneDue is a YYYY-MM-DD date, or empty.
	MilestoneDue string
	// Generation is the revive generation; 0 for a first push.
	Generation int
}

// Content renders the task title: a markdown link to the issue followed by the
// issue title. Markdown links render in Todoist task content.
func (in IssueInput) Content() string {
	title := strings.TrimSpace(in.Title)
	link := fmt.Sprintf("[%s](%s)", in.Ref, in.URL)
	if title == "" {
		return link
	}
	return link + " " + title
}

// Description renders the task description: the head of the issue body,
// truncated at DescriptionLimit runes with an ellipsis marking the cut.
func (in IssueInput) Description() string {
	body := strings.TrimSpace(strings.ReplaceAll(in.Body, "\r\n", "\n"))
	runes := []rune(body)
	if len(runes) <= DescriptionLimit {
		return body
	}
	return strings.TrimRight(string(runes[:DescriptionLimit]), " \t\n") + "…"
}

// BuildItemAdd assembles the item_add command for an issue. projectID may be
// empty, in which case Todoist files the task in the Inbox.
func BuildItemAdd(in IssueInput, projectID, label string) Command {
	args := ItemAddArgs{
		Content:     in.Content(),
		Description: in.Description(),
		ProjectID:   projectID,
		Labels:      []string{label},
		Priority:    PriorityFor(in.Labels),
	}
	if in.MilestoneDue != "" {
		args.Due = &Due{Date: in.MilestoneDue}
	}
	return NewItemAdd(in.URL, in.Generation, args)
}
