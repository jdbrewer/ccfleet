package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jdbrewer/ccfleet/internal/fleet"
)

// a working session with no transcript activity for this long is flagged as possibly stuck
const stallAfter = 10 * time.Minute

// peek goes beside the list at this width, below it otherwise
const sidePeekWidth = 150

const detailLines = 3

var (
	cWait   = lipgloss.AdaptiveColor{Light: "#b45309", Dark: "#fbbf24"}
	cDone   = lipgloss.AdaptiveColor{Light: "#15803d", Dark: "#4ade80"}
	cWork   = lipgloss.AdaptiveColor{Light: "#1d4ed8", Dark: "#60a5fa"}
	cIdle   = lipgloss.AdaptiveColor{Light: "#9ca3af", Dark: "#6b7280"}
	cWarn   = lipgloss.AdaptiveColor{Light: "#b91c1c", Dark: "#f87171"}
	cStall  = lipgloss.AdaptiveColor{Light: "#c2410c", Dark: "#fb923c"}
	cDim    = lipgloss.AdaptiveColor{Light: "#6b7280", Dark: "#9ca3af"}
	cSel    = lipgloss.AdaptiveColor{Light: "#e5e7eb", Dark: "#374151"}
	cAccent = lipgloss.AdaptiveColor{Light: "#7c3aed", Dark: "#a78bfa"}

	sTitle  = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sHeader = lipgloss.NewStyle().Bold(true).Foreground(cDim)
	sDim    = lipgloss.NewStyle().Foreground(cDim)
	sWarn   = lipgloss.NewStyle().Foreground(cWarn)
	sSel    = lipgloss.NewStyle().Background(cSel)
	sRule   = lipgloss.NewStyle().Foreground(cIdle)
)

func stateStyle(st fleet.State) (string, lipgloss.Style) {
	switch st {
	case fleet.Waiting:
		return "⏸", lipgloss.NewStyle().Foreground(cWait).Bold(true)
	case fleet.Done:
		return "✓", lipgloss.NewStyle().Foreground(cDone).Bold(true)
	case fleet.Working:
		return "●", lipgloss.NewStyle().Foreground(cWork)
	}
	return "○", lipgloss.NewStyle().Foreground(cIdle)
}

func stalled(s fleet.Session, now time.Time) bool {
	return s.State == fleet.Working && !s.Info.LastEventAt.IsZero() && now.Sub(s.Info.LastEventAt) > stallAfter
}

func (m *model) sidePeek() bool { return m.peekOn && m.width >= sidePeekWidth }

func (m *model) bodyHeight() int { return max(m.height-2, 1) } // minus header and footer

func (m *model) listWidth() int {
	if m.sidePeek() {
		return m.width - m.peek.Width - 1
	}
	return m.width
}

func (m *model) listHeight() int {
	h := m.bodyHeight() - detailLines
	if m.peekOn && !m.sidePeek() {
		h -= m.peek.Height + 1
	}
	return max(h, 1)
}

func (m *model) layout() {
	if m.sidePeek() {
		m.peek.Width = m.width / 2
		m.peek.Height = max(m.bodyHeight()-1, 1)
	} else {
		m.peek.Width = m.width
		m.peek.Height = max(m.bodyHeight()*45/100, 3)
	}
	m.peekFor = "" // force a re-fit on next capture
}

// fitWidth truncates each line (ANSI-aware) so a captured pane never wraps the layout.
func fitWidth(s string, w int) string {
	if w <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, w, "") + "\x1b[0m"
	}
	return strings.Join(lines, "\n")
}

func (m *model) View() string {
	if m.width == 0 {
		return ""
	}
	if m.help {
		return m.helpView()
	}
	now := time.Now()
	header := m.headerView()
	list := m.listView(now)
	detail := m.detailView(now)
	left := lipgloss.JoinVertical(lipgloss.Left, list, detail)

	var body string
	switch {
	case m.sidePeek():
		sep := sRule.Render(strings.Repeat("│\n", m.bodyHeight()-1) + "│")
		body = lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(m.listWidth()).Render(left), sep, m.peekView())
	case m.peekOn:
		body = lipgloss.JoinVertical(lipgloss.Left, left, m.peekView())
	default:
		body = left
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, m.footerView())
}

func (m *model) headerView() string {
	var wait, done, work, idle int
	for _, s := range m.all {
		switch s.State {
		case fleet.Waiting:
			wait++
		case fleet.Done:
			done++
		case fleet.Working:
			work++
		default:
			idle++
		}
	}
	part := func(st fleet.State, n int, word string) string {
		g, sty := stateStyle(st)
		if n == 0 {
			sty = sDim
		}
		return sty.Render(fmt.Sprintf("%s %d %s", g, n, word))
	}
	left := sTitle.Render(" ccfleet ") + "  " + strings.Join([]string{
		part(fleet.Waiting, wait, "waiting"), part(fleet.Done, done, "done"),
		part(fleet.Working, work, "working"), part(fleet.Idle, idle, "idle"),
	}, "  ")
	right := sDim.Render(fmt.Sprintf("group:%s  ?:help ", m.group))
	if m.err != nil {
		right = sWarn.Render(m.err.Error()+" ") + right
	}
	gap := max(m.width-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return left + strings.Repeat(" ", gap) + right
}

// listView renders group headers and rows, scrolled so the cursor is visible.
func (m *model) listView(now time.Time) string {
	w, h := m.listWidth(), m.listHeight()
	if !m.loaded {
		return lipgloss.NewStyle().Height(h).Render(sDim.Render("  loading sessions…"))
	}
	if len(m.items) == 0 {
		msg := "  no live Claude Code sessions"
		if m.filter.Value() != "" {
			msg = "  nothing matches /" + m.filter.Value()
		}
		return lipgloss.NewStyle().Height(h).Render(sDim.Render(msg))
	}
	var lines []string
	curLine, last := 0, ""
	for i, s := range m.items {
		if k := groupKey(m.group, s); k != last {
			lines = append(lines, sHeader.Render(" "+strings.ToUpper(k)))
			last = k
		}
		if i == m.cur {
			curLine = len(lines)
		}
		lines = append(lines, m.rowView(s, i == m.cur, w, now))
	}
	// keep the cursor in view, and its group header when it is the first row
	if curLine < m.off+1 {
		m.off = max(curLine-1, 0)
	}
	if curLine >= m.off+h {
		m.off = curLine - h + 1
	}
	m.off = min(m.off, max(len(lines)-h, 0))
	end := min(m.off+h, len(lines))
	out := lines[m.off:end]
	for len(out) < h {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

func (m *model) rowView(s fleet.Session, selected bool, w int, now time.Time) string {
	glyph, gs := stateStyle(s.State)
	age := fleet.Ago(s.StateSince, now)
	ageStyle := sDim
	if stalled(s, now) {
		ageStyle = lipgloss.NewStyle().Foreground(cStall)
	}
	flag := " "
	if len(s.Collisions) > 0 {
		flag = sWarn.Render("⚠")
	}
	cursor := "  "
	if selected {
		cursor = sTitle.Render("▸ ")
	}

	const nameW, repoW, ctxW = 18, 14, 4
	branchW := 24
	fixed := 2 + 2 + 5 + 2 + nameW + repoW + ctxW + 4
	rest := w - fixed - branchW
	if rest < 30 { // narrow: drop branch first
		branchW, rest = 0, w-fixed
	}
	rest = max(rest, 10)

	task := s.Label()
	if s.Team != "" {
		task = "[" + s.Team + "/" + s.Role + "] " + task
	}
	doing := ""
	if h := m.hit(s.ID); h != nil && h.Snippet != "" {
		// a transcript match: show where it is instead of what the session is doing
		taskW := rest * 40 / 100
		if rest < 60 {
			taskW = 0
		}
		snip := "“" + clip(around(h.Snippet, h.Term, 12), rest-taskW-3) + "”"
		doing = highlight(pad(snip, rest-taskW), h.Term)
		task = pad(clip(task, taskW-1), taskW)
	} else if s.State == fleet.Working && s.Info.Action != "" && rest >= 60 {
		taskW := rest * 55 / 100
		doing = sDim.Render(pad(clip(s.Info.Action, rest-taskW-1), rest-taskW))
		task = pad(clip(task, taskW-1), taskW)
	} else {
		task = pad(clip(task, rest), rest)
	}

	ctx := ""
	if s.Info.Tokens > 0 {
		ctx = fmt.Sprintf("%d%%", s.ContextPct())
	}
	ctxStyle := sDim
	if s.ContextPct() >= 85 {
		ctxStyle = sWarn
	}

	row := cursor + gs.Render(glyph) + flag + ageStyle.Render(padLeft(age, 4)) + "  " +
		pad(clip(s.Name, nameW-1), nameW) + sDim.Render(pad(clip(s.Repo, repoW-1), repoW))
	if branchW > 0 {
		row += sDim.Render(pad(clip(s.Info.Branch, branchW-1), branchW))
	}
	row += ctxStyle.Render(padLeft(ctx, ctxW)) + "  " + task + doing
	row = ansi.Truncate(row, w, "")
	if selected {
		return withBackground(row, w)
	}
	return row
}

func (m *model) detailView(now time.Time) string {
	s := m.selected()
	rule := sRule.Render(strings.Repeat("─", m.listWidth()))
	if s == nil {
		return rule + "\n\n"
	}
	where := s.Tmux.Pane
	if s.Window != "" {
		where = "tmux " + s.Tmux.Session + ":" + s.Window + " " + s.Tmux.Pane
	}
	bits := []string{s.State.String() + " " + fleet.Ago(s.StateSince, now), where, tildify(s.Cwd)}
	if s.Info.Tokens > 0 {
		bits = append(bits, fmt.Sprintf("ctx %dk", s.Info.Tokens/1000))
	}
	if s.Info.Model != "" {
		bits = append(bits, s.Info.Model)
	}
	if !s.Info.LastEventAt.IsZero() {
		act := "last activity " + fleet.Ago(s.Info.LastEventAt, now)
		if stalled(*s, now) {
			act = lipgloss.NewStyle().Foreground(cStall).Render(act + " (stuck?)")
		}
		bits = append(bits, act)
	}
	line1 := " " + strings.Join(bits, sDim.Render(" · "))

	line2 := ""
	switch {
	case s.State == fleet.Working && s.Info.Action != "":
		line2 = " doing: " + s.Info.Action
	case s.Info.LastText != "":
		line2 = " said: " + s.Info.LastText
	case s.Info.LastPrompt != "":
		line2 = " asked: " + s.Info.LastPrompt
	}
	line2 = sDim.Render(line2)
	if h := m.hit(s.ID); h != nil && h.Snippet != "" {
		n := "1 hit"
		if h.Count != 1 {
			n = fmt.Sprintf("%d hits", h.Count)
		}
		line2 = sDim.Render(fmt.Sprintf(" ⌕ %s · %s, %s ago: ", n, h.Source, fleet.Ago(h.At, now))) +
			highlight("“"+h.Snippet+"”", h.Term)
	} else if len(s.Collisions) > 0 {
		c := s.Collisions[0]
		if len(s.Collisions) > 1 {
			c += fmt.Sprintf(" (+%d more)", len(s.Collisions)-1)
		}
		line2 = sWarn.Render(" ⚠ " + c)
	}
	w := m.listWidth()
	return rule + "\n" + ansi.Truncate(line1, w, "…") + "\n" + ansi.Truncate(line2, w, "…")
}

func (m *model) peekView() string {
	title := " peek"
	if s := m.selected(); s != nil {
		title += " · " + s.Name + " " + s.Tmux.Pane
	}
	if m.focus == focusPeek {
		title += "  (j/k scroll · h back)"
		title = sTitle.Render(title)
	} else {
		title = sDim.Render(title + "  (l to focus)")
	}
	if !m.sidePeek() {
		title = sRule.Render("──") + title
	}
	body := m.peek.View()
	if s := m.selected(); s != nil && !s.Tmux.Valid() {
		body = sDim.Render("  not running in tmux")
	}
	return lipgloss.JoinVertical(lipgloss.Left, ansi.Truncate(title, m.peek.Width, ""),
		lipgloss.NewStyle().Height(m.peek.Height).MaxHeight(m.peek.Height).Render(body))
}

func (m *model) hitNote() string {
	if m.hitsFor != m.filter.Value() {
		return ", searching transcripts…"
	}
	if len(m.hits) > 0 {
		return fmt.Sprintf(", %d by transcript", len(m.hits))
	}
	return ""
}

func (m *model) footerView() string {
	if m.focus == focusFilter {
		return m.filter.View() + sDim.Render(fmt.Sprintf("   %d of %d sessions%s", len(m.items), len(m.all), m.hitNote()))
	}
	if m.flash != "" {
		return " " + m.flash
	}
	hint := " j/k move · ]/[ next need · ⏎ jump · p peek · x seen · / filter · t group · q quit"
	if f := m.filter.Value(); f != "" {
		hint = fmt.Sprintf(" /%s: %d of %d sessions%s (esc clears) ·", f, len(m.items), len(m.all), m.hitNote()) + hint
	}
	if m.count > 0 {
		hint = fmt.Sprintf(" %d", m.count) + hint
	}
	return sDim.Render(ansi.Truncate(hint, m.width, "…"))
}

func (m *model) helpView() string {
	rows := [][2]string{
		{"j / k", "down / up (counts work: 5j)"},
		{"gg / G", "top / bottom"},
		{"C-d / C-u", "half page down / up"},
		{"C-f / C-b", "page down / up"},
		{"] / [", "next / previous session that needs you"},
		{"enter / o", "jump to the session's tmux pane"},
		{"x", "mark seen (drop it from needs-you without jumping)"},
		{"p", "toggle peek"},
		{"l / h, tab", "focus peek / list; j k gg G C-d C-u scroll the peek"},
		{"/", "search names, titles, and full transcripts: your prompts, replies, tool output"},
		{"", "  (space-separated terms all must match; tab / shift-tab or C-j / C-k move"},
		{"", "   while typing; enter keeps the search and returns to vim keys, esc clears)"},
		{"t", "group by state, tmux window, repo"},
		{"r", "refresh now"},
		{"q", "quit"},
		{"", ""},
		{"⏸ waiting", "blocked on a permission prompt or a question"},
		{"✓ done", "finished a turn since you last looked"},
		{"● working", "busy; orange age means no activity for 10m"},
		{"○ idle", "finished and seen"},
		{"⚠", "collision: same file edited, or same branch active, in one checkout"},
	}
	var b strings.Builder
	b.WriteString(sTitle.Render(" ccfleet keys") + "\n\n")
	for _, r := range rows {
		b.WriteString("  " + lipgloss.NewStyle().Bold(true).Width(14).Render(r[0]) + sDim.Render(r[1]) + "\n")
	}
	b.WriteString("\n" + sDim.Render("  any key to close"))
	return b.String()
}

func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func pad(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func padLeft(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return strings.Repeat(" ", n-w) + s
	}
	return s
}

func tildify(p string) string {
	if home := homeDir(); home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

var sMatch = lipgloss.NewStyle().Bold(true).Foreground(cWait)

// highlight renders s dim with case-insensitive occurrences of term emphasised.
func highlight(s, term string) string {
	if term == "" {
		return sDim.Render(s)
	}
	lower := strings.ToLower(s)
	if len(lower) != len(s) { // non-ASCII case folding changed offsets; skip emphasis
		return sDim.Render(s)
	}
	var b strings.Builder
	for {
		i := strings.Index(lower, term)
		if i < 0 {
			b.WriteString(sDim.Render(s))
			return b.String()
		}
		b.WriteString(sDim.Render(s[:i]) + sMatch.Render(s[i:i+len(term)]))
		s, lower = s[i+len(term):], lower[i+len(term):]
	}
}

// around trims s so term starts about lead characters in, keeping the match visible
// in a narrow column.
func around(s, term string, lead int) string {
	i := strings.Index(strings.ToLower(s), term)
	if i < 0 || len(strings.ToLower(s)) != len(s) {
		return s
	}
	r := []rune(s[:i])
	if len(r) <= lead {
		return s
	}
	return "…" + string(r[len(r)-lead:]) + s[i:]
}

// withBackground paints the selection background under an already-styled row. Each
// styled segment ends in a full reset, which would also clear an outer background, so
// the background is re-applied after every reset.
func withBackground(row string, w int) string {
	probe := sSel.Render(" ")
	bg := probe[:strings.Index(probe, " ")]
	if bg == "" { // no colour support
		return row
	}
	row = strings.ReplaceAll(row, "\x1b[0m", "\x1b[0m"+bg)
	return bg + row + strings.Repeat(" ", max(w-lipgloss.Width(row), 0)) + "\x1b[0m"
}
