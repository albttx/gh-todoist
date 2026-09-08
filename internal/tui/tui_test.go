package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func esc() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEsc} }
func runes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}
func backspace() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyBackspace} }

// TestEscCascade walks the three stages Esc moves through, in the order a user
// hits them. The property under test is that Esc quits only from the last one,
// which is what keeps the binding honest against huh's own help line.
func TestEscCascade(t *testing.T) {
	t.Parallel()

	var c escCascade

	// Nothing typed yet: Esc quits straight away.
	if !c.onKey(esc(), false) {
		t.Fatal("with no filter in play, Esc must quit")
	}

	// "/" starts filtering. huh reports filtering=true from the next key on.
	c.onKey(runes("/"), false)

	// Typing narrows the list; Esc must not quit while the filter has focus.
	if c.onKey(runes("1527"), true); c.filterLen != 4 {
		t.Fatalf("filterLen = %d, want 4", c.filterLen)
	}
	if c.onKey(esc(), true) {
		t.Error("Esc while typing a filter must leave the filter, not quit")
	}

	// Filter applied but no longer focused: Esc clears it rather than quitting.
	if c.onKey(esc(), false) {
		t.Error("Esc with a filter applied must clear it, not quit")
	}
	if c.filterLen != 0 {
		t.Errorf("filterLen = %d, want 0 after the filter was cleared", c.filterLen)
	}

	// Back to the start: Esc quits again.
	if !c.onKey(esc(), false) {
		t.Error("once the filter is cleared, Esc must quit")
	}
}

func TestEscCascadeBackspaceEmptiesTheFilter(t *testing.T) {
	t.Parallel()
	var c escCascade
	c.onKey(runes("ab"), true)
	c.onKey(backspace(), true)
	c.onKey(backspace(), true)
	if c.filterLen != 0 {
		t.Fatalf("filterLen = %d, want 0", c.filterLen)
	}
	// An emptied filter is the same as none, so Esc goes straight to quitting
	// once the input loses focus.
	if c.onKey(esc(), true) {
		t.Error("Esc while still typing must not quit")
	}
	if !c.onKey(esc(), false) {
		t.Error("Esc after an emptied filter must quit")
	}
}

func TestEscCascadeIgnoresTypingOutsideTheFilter(t *testing.T) {
	t.Parallel()
	var c escCascade
	// Navigation keys pressed with the list focused must not look like filter
	// text, or Esc would stop quitting.
	c.onKey(runes("jjjk"), false)
	if c.filterLen != 0 {
		t.Fatalf("filterLen = %d, want 0", c.filterLen)
	}
	if !c.onKey(esc(), false) {
		t.Error("Esc must still quit after list navigation")
	}
}

func TestEscCascadeBackspaceNeverGoesNegative(t *testing.T) {
	t.Parallel()
	var c escCascade
	for range 5 {
		c.onKey(backspace(), true)
	}
	if c.filterLen != 0 {
		t.Fatalf("filterLen = %d, want 0", c.filterLen)
	}
}
