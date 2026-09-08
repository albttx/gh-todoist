// Package tui holds the interactive selection used by `gh todoist pick`.
package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/albttx/gh-todoist/pkg/ghsrc"
	"github.com/charmbracelet/huh"
)

// ErrAborted is returned when the user quits the picker without confirming.
var ErrAborted = errors.New("aborted")

// SelectIssues presents a multi-select over issues and returns the chosen ones
// in the order they were displayed.
func SelectIssues(title, description string, issues []ghsrc.Issue) ([]ghsrc.Issue, error) {
	if len(issues) == 0 {
		return nil, nil
	}

	options := make([]huh.Option[int], 0, len(issues))
	width := 0
	for _, issue := range issues {
		if n := len(issue.Ref.String()); n > width {
			width = n
		}
	}
	for i, issue := range issues {
		label := fmt.Sprintf("%-*s  %s", width, issue.Ref.String(), truncate(issue.Title, 70))
		options = append(options, huh.NewOption(label, i))
	}

	var chosen []int
	form := huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[int]().
			Title(title).
			Description(description).
			Options(options...).
			Filterable(true).
			Height(min(len(options)+4, 20)).
			Value(&chosen),
	))
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, ErrAborted
		}
		return nil, fmt.Errorf("issue picker: %w", err)
	}

	// chosen carries indices in selection order; restore display order so the
	// push output reads the same way the list did.
	picked := make([]ghsrc.Issue, 0, len(chosen))
	selected := make(map[int]bool, len(chosen))
	for _, i := range chosen {
		selected[i] = true
	}
	for i, issue := range issues {
		if selected[i] {
			picked = append(picked, issue)
		}
	}
	return picked, nil
}

// ConfirmPush asks for a final yes before anything is written to Todoist.
func ConfirmPush(count int, project string) (bool, error) {
	var ok bool
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title(fmt.Sprintf("Push %s to Todoist project %q?", plural(count, "issue"), project)).
			Affirmative("Push").
			Negative("Cancel").
			Value(&ok),
	))
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, ErrAborted
		}
		return false, fmt.Errorf("confirmation: %w", err)
	}
	return ok, nil
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
