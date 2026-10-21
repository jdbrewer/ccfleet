package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jdbrewer/ccfleet/internal/fleet"
)

func testModel() *model {
	now := time.Now()
	m := &model{peek: viewportFor(), width: 120, height: 40}
	m.filter = newFilter()
	m.all = []fleet.Session{
		{ID: "w", Name: "waiter", Repo: "api", State: fleet.Waiting, StateSince: now},
		{ID: "d", Name: "doner", Repo: "web", State: fleet.Done, StateSince: now},
		{ID: "a", Name: "busy-a", Repo: "api", State: fleet.Working, StateSince: now},
		{ID: "b", Name: "busy-b", Repo: "web", State: fleet.Working, StateSince: now},
		{ID: "i", Name: "sleeper", Repo: "api", State: fleet.Idle, StateSince: now},
	}
	m.loaded = true
	m.rebuild()
	m.setCursor(0)
	m.layout()
	return m
}

func press(m *model, keys string) {
	m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(keys)})
}

func TestVimMotions(t *testing.T) {
	m := testModel()
	press(m, "3j") // arrives as one multi-rune message
	if m.sel != "b" {
		t.Errorf("3j selected %q, want b", m.sel)
	}
	press(m, "gg")
	if m.sel != "w" {
		t.Errorf("gg selected %q, want w", m.sel)
	}
	press(m, "G")
	if m.sel != "i" {
		t.Errorf("G selected %q, want i", m.sel)
	}
	press(m, "]") // wraps to the first needs-you
	if m.sel != "w" {
		t.Errorf("] selected %q, want w", m.sel)
	}
	press(m, "]")
	if m.sel != "d" {
		t.Errorf("second ] selected %q, want d", m.sel)
	}
	press(m, "k")
	press(m, "[") // from w, wraps backwards to d
	if m.sel != "d" {
		t.Errorf("[ selected %q, want d", m.sel)
	}
}

func TestFilterKeepsSelection(t *testing.T) {
	m := testModel()
	press(m, "/web")
	if m.focus != focusFilter || m.filter.Value() != "web" {
		t.Fatalf("focus %v filter %q", m.focus, m.filter.Value())
	}
	if len(m.items) != 2 {
		t.Errorf("filter web kept %d items, want 2", len(m.items))
	}
	m.key(tea.KeyMsg{Type: tea.KeyEnter})
	press(m, "j")
	if m.sel != "b" {
		t.Errorf("j after filter selected %q, want b", m.sel)
	}
	m.key(tea.KeyMsg{Type: tea.KeyEsc})
	if len(m.items) != 5 || m.sel != "b" {
		t.Errorf("esc: %d items, sel %q; want 5, b", len(m.items), m.sel)
	}
}

func TestGrouping(t *testing.T) {
	m := testModel()
	press(m, "tt") // state -> window -> repo
	if m.group != byRepo {
		t.Fatalf("group %v", m.group)
	}
	var repos []string
	for _, s := range m.items {
		repos = append(repos, s.Repo)
	}
	want := []string{"api", "api", "api", "web", "web"}
	for i := range want {
		if repos[i] != want[i] {
			t.Fatalf("repo order %v, want %v", repos, want)
		}
	}
	if v := m.View(); v == "" {
		t.Error("empty view")
	}
}

func TestAroundKeepsTermVisible(t *testing.T) {
	got := around("…run the migration, then ping Ada thread", "ada", 5)
	if got != "…ping Ada thread" {
		t.Errorf("around = %q", got)
	}
	if got := around("short Ada", "ada", 12); got != "short Ada" {
		t.Errorf("around = %q", got)
	}
}

func TestTabMovesWhileFiltering(t *testing.T) {
	m := testModel()
	press(m, "/busy")
	m.key(tea.KeyMsg{Type: tea.KeyTab})
	if m.sel != "b" || m.focus != focusFilter {
		t.Errorf("tab: sel %q focus %v, want b while still filtering", m.sel, m.focus)
	}
	m.key(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.sel != "a" {
		t.Errorf("shift+tab: sel %q, want a", m.sel)
	}
	m.key(tea.KeyMsg{Type: tea.KeyEnter}) // keep filter, back to vim keys
	press(m, "j")
	if m.sel != "b" || m.focus != focusList {
		t.Errorf("after enter, j: sel %q focus %v", m.sel, m.focus)
	}
}

func TestDarkBackground(t *testing.T) {
	cases := []struct {
		env, tmux string
		dark, ok  bool
	}{
		{"", "light", false, true},
		{"", "dark", true, true},
		{"dark", "light", true, true}, // env overrides tmux
		{"", "", false, false},        // unknown: let lipgloss ask the terminal
	}
	for _, c := range cases {
		if d, ok := darkBackground(c.env, c.tmux); d != c.dark || ok != c.ok {
			t.Errorf("darkBackground(%q,%q) = %v,%v", c.env, c.tmux, d, ok)
		}
	}
}
