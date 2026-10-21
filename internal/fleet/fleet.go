package fleet

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// State is what a session needs from you.
type State int

const (
	Waiting State = iota // blocked on a permission prompt or question
	Done                 // finished a turn you haven't looked at
	Working
	Idle
)

func (s State) String() string {
	return [...]string{"waiting", "done", "working", "idle"}[s]
}

// NeedsYou reports whether the state belongs in the needs-you queue.
func (s State) NeedsYou() bool { return s == Waiting || s == Done }

func (s State) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// Session is one live Claude Code session joined with tmux, transcript and team data.
type Session struct {
	ID         string         `json:"sessionId"`
	PID        int            `json:"pid"`
	Name       string         `json:"name"`
	Cwd        string         `json:"cwd"`
	Repo       string         `json:"repo"`
	RepoRoot   string         `json:"repoRoot,omitempty"`
	Version    string         `json:"version"`
	Status     string         `json:"status"` // raw status from Claude Code
	State      State          `json:"state"`
	StateSince time.Time      `json:"stateSince"`
	Tmux       TmuxTarget     `json:"tmux"`
	Window     string         `json:"window,omitempty"` // "index:name"
	Visible    bool           `json:"visible"`
	Team       string         `json:"team,omitempty"`
	Role       string         `json:"role,omitempty"`
	Collisions []string       `json:"collisions,omitempty"`
	Info       TranscriptInfo `json:"transcript"`
}

// Label is the best one-line description of what the session is about.
func (s Session) Label() string {
	if s.Info.Title != "" {
		return s.Info.Title
	}
	if s.Info.LastPrompt != "" {
		return s.Info.LastPrompt
	}
	return s.Name
}

// ContextPct estimates how full the context window is.
func (s Session) ContextPct() int {
	window := 200_000
	if strings.Contains(s.Info.Model, "[1m]") || s.Info.Tokens > window {
		window = 1_000_000
	}
	return s.Info.Tokens * 100 / window
}

// Config points the fleet at its inputs. Zero values use the defaults.
type Config struct {
	ClaudeDir string // ~/.claude
	StateFile string // seen store
	Index     bool   // keep transcript text in memory for Search
}

func (c Config) withDefaults() Config {
	home, _ := os.UserHomeDir()
	if c.ClaudeDir == "" {
		if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
			c.ClaudeDir = d
		} else {
			c.ClaudeDir = filepath.Join(home, ".claude")
		}
	}
	if c.StateFile == "" {
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			base = filepath.Join(home, ".local", "state")
		}
		c.StateFile = filepath.Join(base, "ccfleet", "seen.json")
	}
	return c
}

// Fleet holds the caches that make repeated snapshots cheap.
type Fleet struct {
	mu    sync.Mutex
	cfg   Config
	alive procAlive
	tails map[string]*transcriptTail
	roots map[string]string // cwd -> git toplevel
	teams *teamIndex
	seen  *seenStore
	panes func() map[string]Pane
	now   func() time.Time
}

func New(cfg Config) *Fleet {
	cfg = cfg.withDefaults()
	f := &Fleet{
		cfg:   cfg,
		tails: map[string]*transcriptTail{},
		roots: map[string]string{},
		teams: newTeamIndex(filepath.Join(cfg.ClaudeDir, "teams")),
		seen:  loadSeen(cfg.StateFile),
		panes: ListPanes,
		now:   time.Now,
	}
	f.alive = defaultProcAlive
	return f
}

// Snapshot returns every live session, needs-you first (oldest first), then working, then idle.
func (f *Fleet) Snapshot() ([]Session, error) { return f.snapshot(false) }

// States is a cheap snapshot for the status-line badge: session files, tmux and the seen
// store only. Transcript, git and team fields are left empty.
func (f *Fleet) States() ([]Session, error) { return f.snapshot(true) }

func (f *Fleet) snapshot(lite bool) ([]Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	panes := f.panes()
	raws, parsed, err := loadSessions(filepath.Join(f.cfg.ClaudeDir, "sessions"), f.alive)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if !parsed || os.Getenv("CCFLEET_SOURCE") == "cli" {
		if raws, err = loadFromCLI(f.alive, panes); err != nil {
			return nil, err
		}
	}
	if !lite {
		f.teams.refresh()
	}
	firstRun := f.seen.empty()

	live := map[string]bool{}
	out := make([]Session, 0, len(raws))
	for _, r := range raws {
		live[r.SessionID] = true
		s := Session{
			ID: r.SessionID, PID: r.PID, Name: r.Name, Cwd: r.Cwd, Version: r.Version,
			Status: r.Status, StateSince: msTime(r.StatusUpdatedAt), Tmux: ParseTmuxTarget(r.Tmux),
		}
		if s.StateSince.IsZero() {
			s.StateSince = msTime(r.StartedAt)
		}
		s.Repo = filepath.Base(r.Cwd)
		if !lite {
			if s.RepoRoot = f.repoRoot(r.Cwd); s.RepoRoot != "" {
				s.Repo = filepath.Base(s.RepoRoot)
			}
		}
		if p, ok := panes[s.Tmux.Pane]; ok {
			s.Window = p.WindowIndex + ":" + p.WindowName
			s.Visible = p.Visible
			if p.Visible {
				f.seen.mark(s.ID, now)
			} else if p.SeenAt > 0 {
				f.seen.mark(s.ID, time.UnixMilli(p.SeenAt))
			}
		}
		if firstRun && r.Status == "idle" {
			f.seen.mark(s.ID, now)
		}
		if !lite {
			s.Team, s.Role = f.teams.lookup(s.ID, s.Tmux.Pane)
			s.Info = f.transcript(r.SessionID, now)
		}
		if r.StatusUpdatedAt == 0 && !s.Info.LastEventAt.IsZero() {
			s.StateSince = s.Info.LastEventAt // CLI fallback has no status timestamp
		}
		s.State = deriveState(r.Status, s.StateSince, f.seen.at(s.ID))
		out = append(out, s)
	}
	for id := range f.tails {
		if !live[id] {
			delete(f.tails, id)
		}
	}
	f.seen.prune(live, now)
	f.seen.save()
	findCollisions(out)
	SortSessions(out)
	return out, nil
}

func deriveState(status string, since, seenAt time.Time) State {
	switch status {
	case "waiting":
		return Waiting
	case "busy", "shell":
		return Working
	case "idle":
		if since.After(seenAt) {
			return Done
		}
	}
	return Idle
}

// SortSessions orders sessions waiting, done, working, idle; oldest state first within
// the needs-you states, most recent first otherwise.
func SortSessions(ss []Session) {
	sort.SliceStable(ss, func(i, j int) bool {
		a, b := ss[i], ss[j]
		if a.State != b.State {
			return a.State < b.State
		}
		if a.State.NeedsYou() {
			return a.StateSince.Before(b.StateSince)
		}
		return a.StateSince.After(b.StateSince)
	})
}

// MarkSeen records that the user looked at session id now.
func (f *Fleet) MarkSeen(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen.mark(id, f.now())
	f.seen.save()
}

func (f *Fleet) transcript(id string, now time.Time) TranscriptInfo {
	t, ok := f.tails[id]
	if !ok {
		p := findTranscript(filepath.Join(f.cfg.ClaudeDir, "projects"), id)
		if p == "" {
			return TranscriptInfo{}
		}
		t = newTranscriptTail(p, f.cfg.Index)
		f.tails[id] = t
	}
	_ = t.Refresh(now)
	info := t.info
	info.Edits = append([]Edit(nil), t.info.Edits...)
	return info
}

func (f *Fleet) repoRoot(cwd string) string {
	if r, ok := f.roots[cwd]; ok {
		return r
	}
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	r := ""
	if err == nil {
		r = strings.TrimSpace(string(out))
	}
	f.roots[cwd] = r
	return r
}

// findCollisions flags sessions sharing a checkout that edited the same file recently,
// or that are working on the same branch at the same time.
func findCollisions(ss []Session) {
	type key struct{ root, path string }
	editors := map[key][]int{}
	for i, s := range ss {
		if s.RepoRoot == "" {
			continue
		}
		seen := map[string]bool{}
		for _, e := range s.Info.Edits {
			if !seen[e.Path] {
				seen[e.Path] = true
				editors[key{s.RepoRoot, e.Path}] = append(editors[key{s.RepoRoot, e.Path}], i)
			}
		}
	}
	for k, idx := range editors {
		if len(idx) < 2 {
			continue
		}
		rel, err := filepath.Rel(k.root, k.path)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = k.path
		}
		for _, i := range idx {
			ss[i].Collisions = append(ss[i].Collisions, "file "+rel+" also edited by "+others(ss, idx, i))
		}
	}

	type bkey struct{ root, branch string }
	branches := map[bkey][]int{}
	for i, s := range ss {
		if s.RepoRoot != "" && s.Info.Branch != "" && s.State == Working {
			k := bkey{s.RepoRoot, s.Info.Branch}
			branches[k] = append(branches[k], i)
		}
	}
	for k, idx := range branches {
		if len(idx) < 2 {
			continue
		}
		for _, i := range idx {
			ss[i].Collisions = append(ss[i].Collisions, "branch "+k.branch+" also active in "+others(ss, idx, i))
		}
	}
	for i := range ss {
		sort.Strings(ss[i].Collisions)
	}
}

func others(ss []Session, idx []int, self int) string {
	var names []string
	for _, j := range idx {
		if j != self {
			names = append(names, ss[j].Name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// Ago renders a compact age like 4s, 12m, 3h, 2d.
func Ago(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}
