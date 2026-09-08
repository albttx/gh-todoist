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

// confirmHint is appended to the confirm form's description, because huh builds
// its help line from the focused field's own key bindings and the form-level
// quit binding never appears there. The picker's equivalent is modal and lives
// on keyPlan.Hint.
const confirmHint = "esc or ctrl+c to quit without pushing"

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

// keyPlan is how the picker should treat one keypress, and what the hint line
// should say once it has been handled.
type keyPlan struct {
	// EscQuits is whether the form-level quit binding should include Esc.
	EscQuits bool
	// EnterToggles is whether this Enter selects the highlighted issue instead
	// of submitting the form.
	EnterToggles bool
	// Hint describes the state this key leaves behind.
	Hint string
}

// pickerMode mirrors just enough of huh's MultiSelect state to make Esc and
// Enter mean what the hint line says they mean.
//
// Both keys are modal. Esc runs a three-stage cascade — leave the filter input,
// clear the filter, quit — and Enter selects while a filter is narrowing the
// list but pushes when it is not. Only the first of those distinctions is
// observable through huh's exported API (MultiSelect.GetFiltering), so the
// length of the filter text is mirrored from the key stream to recover the rest.
// Without it the picker would silently push when the user meant to select.
type pickerMode struct {
	filterLen int
}

// filtering means the filter input has focus and is swallowing keystrokes.
// filterActive means a filter is narrowing the list, whether or not it has focus.
func (p *pickerMode) filterActive() bool { return p.filterLen > 0 }

// onKey folds one key into the mode and reports how to treat it. filtering is
// huh's state before the key is applied.
func (p *pickerMode) onKey(k tea.KeyMsg, filtering bool) keyPlan {
	// Both decisions are read off the state as it stands before this key.
	plan := keyPlan{
		EscQuits:     !filtering && !p.filterActive(),
		EnterToggles: k.Type == tea.KeyEnter && !filtering && p.filterActive(),
	}

	switch k.Type {
	case tea.KeyEsc:
		if !filtering && p.filterActive() {
			p.filterLen = 0 // huh is about to clear the filter
		}
	case tea.KeyRunes:
		if filtering {
			p.filterLen += len(k.Runes)
		}
	case tea.KeySpace:
		if filtering {
			p.filterLen++
		}
	case tea.KeyBackspace, tea.KeyDelete:
		if filtering && p.filterLen > 0 {
			p.filterLen--
		}
	}

	plan.Hint = hintFor(afterFiltering(k, filtering), p.filterActive())
	return plan
}

// afterFiltering reports whether the filter input still has focus once this key
// has been handled, so the hint can describe the state the user is about to see
// rather than the one they just left.
func afterFiltering(k tea.KeyMsg, filtering bool) bool {
	switch {
	case filtering && (k.Type == tea.KeyEsc || k.Type == tea.KeyEnter || k.Type == tea.KeyDown):
		// huh's SetFilter: hand focus back to the list, keeping the filter.
		return false
	case !filtering && k.Type == tea.KeyRunes && string(k.Runes) == "/":
		// huh's Filter: focus the filter input.
		return true
	default:
		return filtering
	}
}

// hintFor renders the hint line for a state. Enter is modal, so saying which of
// the two things it does right now is the whole point of this line.
func hintFor(filtering, filterActive bool) string {
	switch {
	case filtering:
		return "type to filter · ↓/esc back to the list · ctrl+c quit"
	case filterActive:
		return "enter/space select · esc clears the filter · ctrl+c quit"
	default:
		return "enter push · space select · / filter · esc quit"
	}
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
		Options(options...).
		// The option key is the whole rendered line, "owner/repo#N  title", so
		// typing either a ref fragment or a word from the title narrows the list.
		Filterable(true).
		Height(min(len(options)+4, 20)).
		Value(&chosen)

	var mode pickerMode
	setHint := func(plan keyPlan) {
		multi.Description(description + "\n" + plan.Hint)
	}
	setHint(keyPlan{Hint: hintFor(false, false)})

	km := huh.NewDefaultKeyMap()
	setEscQuits(km, true)
	// Down leaves the filter input for the list, keeping the filter applied.
	// huh enables SetFilter only while the filter input has focus and
	// key.Matches ignores disabled bindings, so Down still scrolls the list at
	// every other moment.
	km.MultiSelect.SetFilter = key.NewBinding(
		key.WithKeys("enter", "esc", "down"),
		key.WithHelp("↓/esc", "back to the list"),
		key.WithDisabled(),
	)

	// Rebind Esc and reinterpret Enter for each keypress, just before huh sees
	// it. The field's own keymap is copied by value when the form is built, so
	// it cannot be rebound from out here; rewriting the message is what makes
	// Enter modal.
	modal := func(_ tea.Model, msg tea.Msg) tea.Msg {
		k, ok := msg.(tea.KeyMsg)
		if !ok {
			return msg
		}
		// GetFiltering reports the state before this key is applied.
		plan := mode.onKey(k, multi.GetFiltering())
		setEscQuits(km, plan.EscQuits)
		setHint(plan)
		if plan.EnterToggles {
			// huh's Toggle binding is " " and "x"; KeySpace stringifies to " ".
			return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		}
		return msg
	}

	form := huh.NewForm(huh.NewGroup(multi)).
		WithKeyMap(km).
		WithProgramOptions(append(defaultProgramOptions(), tea.WithFilter(modal))...)

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
