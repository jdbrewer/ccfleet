// Package tui is the interactive board: a grouped session list, a live peek at the
// selected pane, and vim-style navigation.
package tui

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jdbrewer/ccfleet/internal/fleet"
)

const refreshEvery = time.Second

type focus int

const (
	focusList focus = iota
	focusPeek
	focusFilter
)

type grouping int

const (
	byState grouping = iota
	byWindow
	byRepo
)

func (g grouping) String() string { return [...]string{"state", "window", "repo"}[g] }

type model struct {
	f      *fleet.Fleet
	client string // tmux client tty; set when launched from a popup

	all   []fleet.Session
	items []fleet.Session // filtered, grouped display order
	sel   string          // selected session id, stable across refreshes
	cur   int             // index of sel in items
	off   int             // first visible list line

	width, height int
	focus         focus
	group         grouping
	filter        textinput.Model
	peekOn        bool
	peek          viewport.Model
	peekFor       string
	help          bool

	hits    map[string]fleet.Hit // transcript matches for hitsFor
	hitsFor string

	count    int  // vim count prefix, e.g. 5j
	pendingG bool // first g of gg
	flash    string
	err      error
	loaded   bool
}

type snapMsg struct {
	ss  []fleet.Session
	err error
}
type tickMsg struct{}
type peekMsg struct{ id, text string }
type jumpedMsg struct{ err error }
type searchMsg struct {
	query string
	hits  map[string]fleet.Hit
}

// Run starts the board. client is the tmux client to jump on; when set the board
// assumes it lives in a popup and exits after a jump.
func Run(f *fleet.Fleet, client string) error {
	if dark, ok := darkBackground(os.Getenv("CCFLEET_THEME"), fleet.ClientTheme(client)); ok {
		lipgloss.SetHasDarkBackground(dark)
	}
	m := &model{f: f, client: client, filter: newFilter(), peekOn: true, peek: viewportFor()}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// darkBackground picks the colour theme: CCFLEET_THEME wins, then tmux's client_theme.
// When neither is known, lipgloss falls back to asking the terminal, which a tmux popup
// doesn't answer reliably.
func darkBackground(env, tmuxTheme string) (dark, ok bool) {
	for _, v := range []string{env, tmuxTheme} {
		switch strings.ToLower(v) {
		case "dark":
			return true, true
		case "light":
			return false, true
		}
	}
	return false, false
}

func newFilter() textinput.Model {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "filter: name, repo, branch, task, state…"
	return ti
}

func viewportFor() viewport.Model { return viewport.New(0, 0) }

func (m *model) Init() tea.Cmd { return m.snapshot() }

func (m *model) snapshot() tea.Cmd {
	return func() tea.Msg {
		ss, err := m.f.Snapshot()
		return snapMsg{ss, err}
	}
}

// search runs the transcript search off the UI goroutine.
func (m *model) search() tea.Cmd {
	q := m.filter.Value()
	if strings.TrimSpace(q) == "" {
		return nil
	}
	return func() tea.Msg { return searchMsg{q, m.f.Search(q)} }
}

func (m *model) hit(id string) *fleet.Hit {
	if h, ok := m.hits[id]; ok {
		return &h
	}
	return nil
}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *model) capturePeek() tea.Cmd {
	s := m.selected()
	if !m.peekOn || s == nil || !s.Tmux.Valid() {
		return nil
	}
	id, pane, lines := s.ID, s.Tmux.Pane, max(m.peek.Height, 10)+200
	return func() tea.Msg { return peekMsg{id, fleet.Capture(pane, lines)} }
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, m.capturePeek()
	case snapMsg:
		m.err = msg.err
		if msg.err == nil {
			m.all = msg.ss
			m.rebuild()
		}
		if !m.loaded {
			m.loaded = true
			m.selectFirst()
		}
		return m, tea.Batch(tick(), m.capturePeek(), m.search())
	case searchMsg:
		if msg.query == m.filter.Value() {
			m.hits, m.hitsFor = msg.hits, msg.query
			m.rebuild()
			return m, m.capturePeek()
		}
		return m, nil
	case tickMsg:
		return m, m.snapshot()
	case peekMsg:
		if msg.id == m.sel {
			atBottom := m.peek.AtBottom() || m.peekFor != msg.id
			m.peek.SetContent(fitWidth(msg.text, m.peek.Width))
			if atBottom {
				m.peek.GotoBottom()
			}
			m.peekFor = msg.id
		}
		return m, nil
	case jumpedMsg:
		if msg.err != nil {
			m.flash = "jump failed: " + msg.err.Error()
			return m, nil
		}
		if m.client != "" {
			return m, tea.Quit
		}
		return m, m.snapshot()
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m *model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	m.flash = ""
	if m.help {
		m.help = false
		return m, nil
	}
	if m.focus == focusFilter {
		return m.filterKey(k)
	}
	// fast input can arrive as one multi-rune message ("5j"); treat each rune as a key
	if k.Type == tea.KeyRunes && len(k.Runes) > 1 && !k.Paste {
		var cmds []tea.Cmd
		for i, r := range k.Runes {
			_, cmd := m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			cmds = append(cmds, cmd)
			if m.focus == focusFilter { // "/" mid-batch: the rest is filter text
				m.filter.SetValue(string(k.Runes[i+1:]))
				m.filter.CursorEnd()
				m.rebuild()
				cmds = append(cmds, m.search())
				break
			}
		}
		return m, tea.Batch(cmds...)
	}

	s := k.String()
	if len(s) == 1 && s[0] >= '1' && s[0] <= '9' || (s == "0" && m.count > 0) {
		m.count = m.count*10 + int(s[0]-'0')
		return m, nil
	}
	n := max(m.count, 1)
	m.count = 0
	if s == "g" {
		if m.pendingG {
			m.pendingG = false
			return m.motion("top", n)
		}
		m.pendingG = true
		return m, nil
	}
	m.pendingG = false

	switch s {
	case "q":
		return m, tea.Quit
	case "?":
		m.help = true
		return m, nil
	case "/":
		m.focus = focusFilter
		m.layout()
		return m, m.filter.Focus()
	case "esc":
		if m.focus == focusPeek {
			m.focus = focusList
		} else if m.filter.Value() != "" {
			m.filter.SetValue("")
			m.rebuild()
		}
		m.layout()
		return m, m.capturePeek()
	case "p":
		m.peekOn = !m.peekOn
		if !m.peekOn {
			m.focus = focusList
		}
		m.layout()
		return m, m.capturePeek()
	case "t":
		m.group = (m.group + 1) % 3
		m.rebuild()
		m.flash = "group by " + m.group.String()
		return m, nil
	case "r":
		return m, m.snapshot()
	case "enter", "o":
		return m, m.jump()
	case "x":
		if sel := m.selected(); sel != nil {
			m.f.MarkSeen(sel.ID)
			m.flash = "marked seen: " + sel.Name
			return m, m.snapshot()
		}
		return m, nil
	}

	if m.focus == focusPeek {
		return m.peekKey(s, n)
	}
	switch s {
	case "j", "down":
		return m.motion("down", n)
	case "k", "up":
		return m.motion("up", n)
	case "G", "end":
		return m.motion("bottom", n)
	case "home":
		return m.motion("top", n)
	case "ctrl+d":
		return m.motion("down", max(m.listHeight()/2, 1)*n)
	case "ctrl+u":
		return m.motion("up", max(m.listHeight()/2, 1)*n)
	case "ctrl+f", "pgdown":
		return m.motion("down", max(m.listHeight()-1, 1)*n)
	case "ctrl+b", "pgup":
		return m.motion("up", max(m.listHeight()-1, 1)*n)
	case "]":
		return m.motion("nextNeed", n)
	case "[":
		return m.motion("prevNeed", n)
	case "l", "right", "tab":
		if m.peekOn {
			m.focus = focusPeek
		}
		return m, nil
	}
	return m, nil
}

func (m *model) peekKey(s string, n int) (tea.Model, tea.Cmd) {
	switch s {
	case "h", "left", "tab":
		m.focus = focusList
	case "j", "down":
		m.peek.LineDown(n)
	case "k", "up":
		m.peek.LineUp(n)
	case "ctrl+d":
		m.peek.HalfViewDown()
	case "ctrl+u":
		m.peek.HalfViewUp()
	case "ctrl+f", "pgdown":
		m.peek.ViewDown()
	case "ctrl+b", "pgup":
		m.peek.ViewUp()
	case "G", "end":
		m.peek.GotoBottom()
	}
	return m, nil
}

func (m *model) filterKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.filter.SetValue("")
		m.filter.Blur()
		m.focus = focusList
		m.rebuild()
		m.layout()
		return m, m.capturePeek()
	case "enter":
		m.filter.Blur()
		m.focus = focusList
		m.layout()
		return m, m.capturePeek()
	case "tab", "ctrl+n", "ctrl+j", "down":
		return m.motion("down", 1)
	case "shift+tab", "ctrl+p", "ctrl+k", "up":
		return m.motion("up", 1)
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(k)
	m.rebuild()
	return m, tea.Batch(cmd, m.capturePeek(), m.search())
}

func (m *model) motion(kind string, n int) (tea.Model, tea.Cmd) {
	if len(m.items) == 0 {
		return m, nil
	}
	c := m.cur
	switch kind {
	case "down":
		c = min(c+n, len(m.items)-1)
	case "up":
		c = max(c-n, 0)
	case "top":
		c = 0
	case "bottom":
		c = len(m.items) - 1
	case "nextNeed", "prevNeed":
		step := 1
		if kind == "prevNeed" {
			step = -1
		}
		found := false
		for i, j := 0, c; i < len(m.items) && n > 0; i++ {
			j = (j + step + len(m.items)) % len(m.items)
			if m.items[j].State.NeedsYou() {
				c, n, found = j, n-1, true
			}
		}
		if !found {
			m.flash = "nothing needs you"
		}
	}
	m.setCursor(c)
	return m, m.capturePeek()
}

func (m *model) setCursor(c int) {
	m.cur = c
	if c >= 0 && c < len(m.items) {
		if m.sel != m.items[c].ID {
			m.peekFor = ""
		}
		m.sel = m.items[c].ID
	}
}

func (m *model) selected() *fleet.Session {
	if m.cur >= 0 && m.cur < len(m.items) && m.items[m.cur].ID == m.sel {
		return &m.items[m.cur]
	}
	return nil
}

// selectFirst puts the cursor on the oldest session that needs you, else the top.
func (m *model) selectFirst() {
	for i, s := range m.items {
		if s.State.NeedsYou() {
			m.setCursor(i)
			return
		}
	}
	m.setCursor(0)
}

func (m *model) jump() tea.Cmd {
	s := m.selected()
	if s == nil {
		return nil
	}
	if !s.Tmux.Valid() {
		m.flash = s.Name + " is not in tmux"
		return nil
	}
	m.f.MarkSeen(s.ID)
	pane, client := s.Tmux.Pane, m.client
	return func() tea.Msg { return jumpedMsg{fleet.Jump(pane, client)} }
}

// rebuild applies the filter and grouping to m.all, keeping the selection.
func (m *model) rebuild() {
	q := m.filter.Value()
	if q != m.hitsFor {
		m.hits = nil // stale until the search for q returns
	}
	items := m.items[:0:0]
	for _, s := range m.all {
		if fleet.Matches(s, q, m.hit(s.ID)) {
			items = append(items, s)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		return groupLess(m.group, items[i], items[j])
	})
	m.items = items
	m.cur = 0
	for i, s := range items {
		if s.ID == m.sel {
			m.cur = i
			return
		}
	}
	m.setCursor(min(m.cur, len(items)-1))
}

func groupKey(g grouping, s fleet.Session) string {
	switch g {
	case byWindow:
		if s.Window == "" {
			return "not in tmux"
		}
		return "window " + s.Tmux.Session + ":" + s.Window
	case byRepo:
		return s.Repo
	}
	switch s.State {
	case fleet.Waiting, fleet.Done:
		return "needs you"
	case fleet.Working:
		return "working"
	}
	return "idle"
}

func groupLess(g grouping, a, b fleet.Session) bool {
	switch g {
	case byWindow:
		if a.Tmux.Session != b.Tmux.Session {
			return a.Tmux.Session < b.Tmux.Session
		}
		ai, bi := windowIndex(a), windowIndex(b)
		return ai < bi
	case byRepo:
		return a.Repo < b.Repo
	}
	return false // m.all is already in state order
}

func windowIndex(s fleet.Session) int {
	i, err := strconv.Atoi(strings.SplitN(s.Window, ":", 2)[0])
	if err != nil {
		return 1 << 30
	}
	return i
}
