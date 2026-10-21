package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// teamIndex maps sessions to agent teams from ~/.claude/teams/*/config.json.
// The lead is known by session id, tmux-backed teammates by pane id.
type teamIndex struct {
	dir      string
	loadedAt time.Time
	byLead   map[string]string    // lead session id -> team
	byPane   map[string][2]string // pane id -> {team, member name}
}

const teamRefresh = 10 * time.Second

func newTeamIndex(dir string) *teamIndex { return &teamIndex{dir: dir} }

type rawTeam struct {
	Name          string `json:"name"`
	LeadSessionID string `json:"leadSessionId"`
	Members       []struct {
		Name       string `json:"name"`
		AgentType  string `json:"agentType"`
		TmuxPaneID string `json:"tmuxPaneId"`
	} `json:"members"`
}

func (t *teamIndex) refresh() {
	if time.Since(t.loadedAt) < teamRefresh {
		return
	}
	t.loadedAt = time.Now()
	t.byLead, t.byPane = map[string]string{}, map[string][2]string{}
	files, _ := filepath.Glob(filepath.Join(t.dir, "*", "config.json"))
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var tm rawTeam
		if json.Unmarshal(b, &tm) != nil {
			continue
		}
		mates := 0
		for _, m := range tm.Members {
			if m.AgentType == "team-lead" {
				continue
			}
			mates++
			if len(m.TmuxPaneID) > 1 && m.TmuxPaneID[0] == '%' {
				t.byPane[m.TmuxPaneID] = [2]string{tm.Name, m.Name}
			}
		}
		// every session gets an implicit solo team; only real teams are worth showing
		if mates > 0 && tm.LeadSessionID != "" {
			t.byLead[tm.LeadSessionID] = tm.Name
		}
	}
}

func (t *teamIndex) lookup(sessionID, pane string) (team, role string) {
	if name, ok := t.byLead[sessionID]; ok {
		return name, "lead"
	}
	if m, ok := t.byPane[pane]; ok {
		return m[0], m[1]
	}
	return "", ""
}
