// Package fleet reads Claude Code's on-disk session state and joins it with tmux.
//
// Everything under ~/.claude is an internal Claude Code format. Each file kind is
// parsed in exactly one place so a format change breaks one function, not the tool.
package fleet

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// rawSession mirrors ~/.claude/sessions/<pid>.json.
type rawSession struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	Cwd             string `json:"cwd"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Version         string `json:"version"`
	Status          string `json:"status"`
	StartedAt       int64  `json:"startedAt"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
	Tmux            string `json:"tmux"`
}

// TmuxTarget is a parsed "session:@window.%pane" string.
type TmuxTarget struct {
	Session string `json:"session"`
	Window  string `json:"window"`
	Pane    string `json:"pane"`
}

func (t TmuxTarget) Valid() bool { return t.Pane != "" }

// ParseTmuxTarget parses the sessions file's tmux field, e.g. "1:@36.%147".
func ParseTmuxTarget(s string) TmuxTarget {
	var t TmuxTarget
	colon := strings.LastIndex(s, ":")
	if colon < 0 {
		return t
	}
	t.Session = s[:colon]
	rest := s[colon+1:]
	dot := strings.Index(rest, ".")
	if dot < 0 {
		return TmuxTarget{}
	}
	t.Window, t.Pane = rest[:dot], rest[dot+1:]
	if !strings.HasPrefix(t.Pane, "%") {
		return TmuxTarget{}
	}
	return t
}

// procAlive reports which sessions' processes are still running. Keyed by pid.
type procAlive func([]rawSession) map[int]bool

// reuseSlack allows for clock skew between process start and the session file's startedAt.
const reuseSlack = time.Minute

// defaultProcAlive keeps sessions whose pid is running and started before the session
// did. A reused pid belongs to a process that started later. The process name is no
// help: Claude Code retitles itself to its version number.
func defaultProcAlive(rs []rawSession) map[int]bool {
	alive := map[int]bool{}
	for _, r := range rs {
		if r.PID <= 0 {
			continue
		}
		start, ok := procStart(r.PID)
		if !ok {
			continue
		}
		if start.IsZero() || r.StartedAt == 0 || !start.After(msTime(r.StartedAt).Add(reuseSlack)) {
			alive[r.PID] = true
		}
	}
	return alive
}

// loadSessions returns live sessions from dir, skipping non-json and dead pids.
// parsed is false when json files exist but none could be read as a session, i.e. the
// format has changed under us.
func loadSessions(dir string, alive procAlive) (live []rawSession, parsed bool, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, err
	}
	var all []rawSession
	files := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		files++
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // removed between ReadDir and ReadFile
		}
		var r rawSession
		if json.Unmarshal(b, &r) != nil || r.SessionID == "" {
			continue
		}
		all = append(all, r)
	}
	ok := alive(all)
	for _, r := range all {
		if ok[r.PID] {
			live = append(live, r)
		}
	}
	return live, files == 0 || len(all) > 0, nil
}

// loadFromCLI asks `claude agents --json`, the documented interface, for the sessions.
// It is slower and has no tmux pane or status timestamp, so it is only the fallback
// for when the session files can't be read; panes are found by process ancestry.
func loadFromCLI(alive procAlive, panes map[string]Pane) ([]rawSession, error) {
	out, err := exec.Command("claude", "agents", "--json").Output()
	if err != nil {
		return nil, fmt.Errorf("session files unreadable and `claude agents --json` failed: %w", err)
	}
	var all []rawSession
	if err := json.Unmarshal(out, &all); err != nil {
		return nil, fmt.Errorf("parse `claude agents --json`: %w", err)
	}
	byPID := map[int]Pane{}
	for _, p := range panes {
		byPID[p.PID] = p
	}
	ok := alive(all)
	var live []rawSession
	for _, r := range all {
		if !ok[r.PID] {
			continue
		}
		for pid, hops := r.PID, 0; pid > 1 && hops < 4; pid, hops = procParent(pid), hops+1 {
			if p, found := byPID[pid]; found {
				r.Tmux = p.Session + ":" + p.WindowID + "." + p.ID
				break
			}
		}
		live = append(live, r)
	}
	return live, nil
}

func msTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
