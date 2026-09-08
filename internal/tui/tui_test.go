package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func esc() tea.KeyMsg       { return tea.KeyMsg{Type: tea.KeyEsc} }
func enter() tea.KeyMsg     { return tea.KeyMsg{Type: tea.KeyEnter} }
func down() tea.KeyMsg      { return tea.KeyMsg{Type: tea.KeyDown} }
func backspace() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyBackspace} }
func runes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// TestPickerFlow walks the exact sequence the picker is designed around:
//
//	/widgets  ->  ↓  ->  enter enter  ->  esc  ->  enter
//
// filter, drop into the list, select two, clear the filter, push.
func TestPickerFlow(t *testing.T) {
	t.Parallel()

	var m pickerMode
	filtering := false

	// A key's plan is read off the state before it lands; huh reports the same
	// thing through GetFiltering, so track it the way the hook does.
	step := func(k tea.KeyMsg) keyPlan {
		p := m.onKey(k, filtering)
		filtering = afterFiltering(k, filtering)
		return p
	}

	// "/" focuses the filter input.
	step(runes("/"))
	if !filtering {
		t.Fatal("\"/\" must focus the filter input")
	}

	// Typing narrows the list. Esc must not quit while the input has focus.
	p := step(runes("widgets"))
	if p.EscQuits {
		t.Error("Esc must not quit while the filter input has focus")
	}

	// Down hands focus back to the list, keeping the filter applied.
	p = step(down())
	if filtering {
		t.Fatal("Down must leave the filter input")
	}
	if !m.filterActive() {
		t.Fatal("Down must keep the filter applied")
	}

	// With a filter narrowing the list, Enter selects rather than pushing.
	for i := range 2 {
		p = step(enter())
		if !p.EnterToggles {
			t.Fatalf("Enter %d must select while a filter is active", i+1)
		}
		if p.EscQuits {
			t.Error("Esc must clear the filter before it quits")
		}
	}

	// Esc clears the filter instead of quitting.
	p = step(esc())
	if p.EscQuits {
		t.Error("Esc with a filter applied must clear it, not quit")
	}
	if m.filterActive() {
		t.Error("the filter should be cleared")
	}

	// Back to an unfiltered list: Enter pushes, Esc quits.
	p = step(enter())
	if p.EnterToggles {
		t.Error("Enter must push once no filter is active")
	}
	if !p.EscQuits {
		t.Error("Esc must quit once no filter is active")
	}
}

func TestEnterIsModal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		filterLen  int
		filtering  bool
		wantToggle bool
	}{
		{name: "no filter, list focused: pushes", wantToggle: false},
		{name: "filter active, list focused: selects", filterLen: 3, wantToggle: true},
		{
			// huh owns Enter here: it applies the filter and returns to the
			// list. Rewriting it into a toggle would select nothing, because
			// toggling is disabled while the input has focus.
			name:       "filter input focused: huh applies the filter",
			filterLen:  3,
			filtering:  true,
			wantToggle: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := pickerMode{filterLen: tt.filterLen}
			if got := m.onKey(enter(), tt.filtering).EnterToggles; got != tt.wantToggle {
				t.Errorf("EnterToggles = %v, want %v", got, tt.wantToggle)
			}
		})
	}
}

func TestEscCascade(t *testing.T) {
	t.Parallel()

	var m pickerMode

	if !m.onKey(esc(), false).EscQuits {
		t.Fatal("with no filter in play, Esc must quit")
	}

	m.onKey(runes("/"), false)
	m.onKey(runes("1527"), true)
	if m.filterLen != 4 {
		t.Fatalf("filterLen = %d, want 4", m.filterLen)
	}
	if m.onKey(esc(), true).EscQuits {
		t.Error("Esc while typing a filter must leave the input, not quit")
	}
	if m.onKey(esc(), false).EscQuits {
		t.Error("Esc with a filter applied must clear it, not quit")
	}
	if m.filterLen != 0 {
		t.Errorf("filterLen = %d, want 0 after the filter was cleared", m.filterLen)
	}
	if !m.onKey(esc(), false).EscQuits {
		t.Error("once the filter is cleared, Esc must quit")
	}
}

func TestAfterFiltering(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		key       tea.KeyMsg
		filtering bool
		want      bool
	}{
		{name: "slash focuses the input", key: runes("/"), want: true},
		{name: "down leaves the input", key: down(), filtering: true},
		{name: "esc leaves the input", key: esc(), filtering: true},
		{name: "enter applies the filter", key: enter(), filtering: true},
		{name: "typing stays in the input", key: runes("a"), filtering: true, want: true},
		{name: "down on the list is just navigation", key: down()},
		{name: "slash while typing is filter text", key: runes("/"), filtering: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := afterFiltering(tt.key, tt.filtering); got != tt.want {
				t.Errorf("afterFiltering = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterLengthMirror(t *testing.T) {
	t.Parallel()

	t.Run("backspace empties the filter", func(t *testing.T) {
		t.Parallel()
		var m pickerMode
		m.onKey(runes("ab"), true)
		m.onKey(backspace(), true)
		m.onKey(backspace(), true)
		if m.filterActive() {
			t.Fatal("filter should be empty")
		}
		// An emptied filter is the same as none: Enter goes back to pushing.
		if m.onKey(enter(), false).EnterToggles {
			t.Error("Enter must push once the filter is empty")
		}
	})

	t.Run("typing outside the input is not filter text", func(t *testing.T) {
		t.Parallel()
		var m pickerMode
		m.onKey(runes("jjjk"), false)
		if m.filterActive() {
			t.Fatal("list navigation must not register as filter text")
		}
		if !m.onKey(esc(), false).EscQuits {
			t.Error("Esc must still quit after list navigation")
		}
	})

	t.Run("backspace never goes negative", func(t *testing.T) {
		t.Parallel()
		var m pickerMode
		for range 5 {
			m.onKey(backspace(), true)
		}
		if m.filterLen != 0 {
			t.Fatalf("filterLen = %d, want 0", m.filterLen)
		}
	})
}

// TestSplitFilterBurst pins the v1.3.0 regression: typing "/docs" quickly, or
// typing it while the picker is still loading, arrives from the terminal as a
// single KeyRunes whose String() is "/docs". huh's filter binding matches the
// literal "/" only, so the filter never opened, the list stayed unfiltered, and
// the next Enter submitted nothing. Splitting the burst is what fixes it.
func TestSplitFilterBurst(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		key       tea.KeyMsg
		filtering bool
		wantSplit bool
		wantStart string
		wantRest  string
	}{
		{
			name:      "the reported bug: /docs arrives as one key",
			key:       runes("/docs"),
			wantSplit: true, wantStart: "/", wantRest: "docs",
		},
		{
			name:      "two runes is enough to break it",
			key:       runes("/d"),
			wantSplit: true, wantStart: "/", wantRest: "d",
		},
		{
			name:      "a lone slash is already correct",
			key:       runes("/"),
			wantSplit: false, wantStart: "/",
		},
		{
			name:      "a burst not starting with slash is list navigation",
			key:       runes("jjk"),
			wantSplit: false, wantStart: "jjk",
		},
		{
			name:      "while filtering, a burst is filter text and must pass through",
			key:       runes("/docs"),
			filtering: true,
			wantSplit: false, wantStart: "/docs",
		},
		{
			name:      "non-rune keys are untouched",
			key:       enter(),
			wantSplit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			start, rest, ok := splitFilterBurst(tt.key, tt.filtering)
			if ok != tt.wantSplit {
				t.Fatalf("split = %v, want %v", ok, tt.wantSplit)
			}
			if tt.wantStart != "" && start.String() != tt.wantStart {
				t.Errorf("start = %q, want %q", start.String(), tt.wantStart)
			}
			if got := string(rest); got != tt.wantRest {
				t.Errorf("rest = %q, want %q", got, tt.wantRest)
			}
		})
	}
}

// TestCoalescedFilterBurstOpensTheFilter replays the reported bug over the pure
// state machine. The burst is split so the "/" reaches huh and the filter opens;
// the glued runes are dropped, so the mirror correctly reports an empty filter
// and Enter still means push. The bug being fixed is the invisible one: in
// v1.3.0 the "/" never reached huh at all, so the picker looked idle and the
// user had no way to tell that the filter had not opened.
func TestCoalescedFilterBurstOpensTheFilter(t *testing.T) {
	t.Parallel()

	var m pickerMode
	filtering := false

	start, rest, ok := splitFilterBurst(runes("/docs"), filtering)
	if !ok {
		t.Fatal("the burst must be recognised")
	}
	if string(rest) != "docs" {
		t.Errorf("rest = %q, want the runes that were glued to the slash", string(rest))
	}

	m.onKey(start, filtering)
	filtering = afterFiltering(start, filtering)
	if !filtering {
		t.Fatal("the split \"/\" must open the filter, which v1.3.0 never did")
	}
	// The dropped runes must not be counted, or Enter would claim to select
	// against a filter that is not actually there.
	if m.filterActive() {
		t.Error("dropped runes must not register as filter text")
	}
}

// TestUnsplitBurstReproducesTheBug documents the old behaviour, so that dropping
// the split would fail here rather than only in a terminal.
func TestUnsplitBurstReproducesTheBug(t *testing.T) {
	t.Parallel()
	var m pickerMode
	// Without splitting, huh never opens the filter, so filtering stays false
	// and the burst is not counted as filter text.
	m.onKey(runes("/docs"), false)
	if m.filterActive() {
		t.Fatal("an unsplit burst cannot have reached the filter")
	}
	if m.onKey(enter(), false).EnterToggles {
		t.Error("without a filter, Enter submits — which is the reported bug")
	}
}
