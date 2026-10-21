// Package fleet reads Claude Code's on-disk session state and joins it with tmux.
//
// Everything under ~/.claude is an internal Claude Code format. Each file kind is
// parsed in exactly one place so a format change breaks one function, not the tool.
package fleet

import (
	"encoding/json"
	"os"
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
func loadSessions(dir string, alive procAlive) ([]rawSession, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var all []rawSession
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
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
	live := alive(all)
	out := all[:0]
	for _, r := range all {
		if live[r.PID] {
			out = append(out, r)
		}
	}
	return out, nil
}

func msTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
