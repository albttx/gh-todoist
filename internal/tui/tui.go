// Package tui holds the interactive selection used by `gh todoist pick`.
package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/albttx/gh-todoist/pkg/ghsrc"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
)

// ErrAborted is returned when the user quits a form without confirming.
var ErrAborted = errors.New("aborted")

// pickerHint and confirmHint are appended to the form descriptions, because huh
// builds its help line from the focused field's own key bindings and the
// form-level quit binding never appears there.
const (
	pickerHint  = "/ filter · esc leaves the filter, then quits · ctrl+c quit"
	confirmHint = "esc or ctrl+c to quit without pushing"
)

// setEscQuits rebuilds the form-level quit binding to include Esc, or not.
//
// huh matches the form-level Quit binding in Form.Update before the key reaches
// the focused field, so binding Esc there unconditionally would shadow the
// multi-select's own Esc handling and make the filter unusable. Toggling the
// binding lets Esc cascade instead: leave the filter first, quit second.
func setEscQuits(km *huh.KeyMap, quits bool) {
	keys := []string{"ctrl+c"}
	if quits {
		keys = append(keys, "esc")
	}
	km.Quit = key.NewBinding(key.WithKeys(keys...), key.WithHelp("esc", "quit"))
}

// defaultProgramOptions repeats huh's own defaults, which WithProgramOptions
// replaces wholesale rather than appends to. Rendering to stderr is the
// load-bearing one: it keeps the interface out of piped stdout.
func defaultProgramOptions() []tea.ProgramOption {
	return []tea.ProgramOption{
		tea.WithOutput(os.Stderr),
		tea.WithReportFocus(),
	}
}

// escCascade tracks how far along Esc's three stages the picker is, so that Esc
// means what huh's own help line says it means at that moment.
//
// The stages are: leave the filter input, clear the filter, quit. Only the first
// is observable through huh's exported API (MultiSelect.GetFiltering), so the
// length of the filter text is mirrored from the key stream to tell the last two
// apart. Without this the help line would lie: huh offers "esc clear filter" in
// a state where an unconditional quit binding would exit the picker instead.
type escCascade struct {
	filterLen int
}

// onKey folds one key into the cascade and reports whether Esc should quit the
// form right now. filtering is the field's state before the key is applied.
func (e *escCascade) onKey(k tea.KeyMsg, filtering bool) (escQuits bool) {
	switch k.Type {
	case tea.KeyEsc:
		// Quit only from the last stage: not typing, and nothing filtered.
		quits := !filtering && e.filterLen == 0
		if !filtering && e.filterLen > 0 {
			e.filterLen = 0 // huh is about to clear the filter
		}
		return quits
	case tea.KeyRunes:
		if filtering {
			e.filterLen += len(k.Runes)
		}
	case tea.KeySpace:
		if filtering {
			e.filterLen++
		}
	case tea.KeyBackspace, tea.KeyDelete:
		if filtering && e.filterLen > 0 {
			e.filterLen--
		}
	}
	// Any other key leaves the cascade where it was; Esc keeps its current
	// meaning, which is "quit" whenever no filter is in play.
	return !filtering && e.filterLen == 0
}

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
	multi := huh.NewMultiSelect[int]().
		Title(title).
		Description(description + "\n" + pickerHint).
		Options(options...).
		// The option key is the whole rendered line, "owner/repo#N  title", so
		// typing either a ref fragment or a word from the title narrows the list.
		Filterable(true).
		Height(min(len(options)+4, 20)).
		Value(&chosen)

	km := huh.NewDefaultKeyMap()
	setEscQuits(km, true)

	// Esc cascades exactly the way huh's own help line advertises it: leave the
	// filter input, then clear the filter, then quit. Only the first of those
	// three is visible through the exported API (GetFiltering), so the length of
	// the filter text is mirrored from the key stream to tell the other two
	// apart. Getting this wrong would make the help line lie — it offers "esc
	// clear filter" in a state where an unconditional quit binding would instead
	// exit the picker.
	var cascade escCascade
	escFilter := func(_ tea.Model, msg tea.Msg) tea.Msg {
		if k, ok := msg.(tea.KeyMsg); ok {
			// GetFiltering reports the state before this key is applied.
			setEscQuits(km, cascade.onKey(k, multi.GetFiltering()))
		}
		return msg
	}

	form := huh.NewForm(huh.NewGroup(multi)).
		WithKeyMap(km).
		WithProgramOptions(append(defaultProgramOptions(), tea.WithFilter(escFilter))...)

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
			Description(confirmHint).
			Affirmative("Push").
			Negative("Cancel").
			Value(&ok),
	))
	// No filter on this screen, so Esc can quit unconditionally.
	confirmKeys := huh.NewDefaultKeyMap()
	setEscQuits(confirmKeys, true)
	form = form.WithKeyMap(confirmKeys)

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
