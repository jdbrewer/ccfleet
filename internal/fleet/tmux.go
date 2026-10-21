package fleet

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Pane is one tmux pane as seen by list-panes.
type Pane struct {
	ID          string // %147
	Session     string
	WindowID    string // @36
	WindowIndex string
	WindowName  string
	PaneIndex   string
	Visible     bool // active pane of the active window of an attached session
	SeenAt      int64
}

const paneFormat = "#{pane_id}\t#{session_name}\t#{window_id}\t#{window_index}\t#{window_name}\t#{pane_index}\t" +
	"#{&&:#{&&:#{pane_active},#{window_active}},#{session_attached}}\t#{@ccfleet_seen}"

// HaveTmux reports whether a tmux server is reachable.
func HaveTmux() bool {
	return exec.Command("tmux", "list-sessions").Run() == nil
}

// ListPanes returns every pane on the server keyed by pane id.
func ListPanes() map[string]Pane {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F", paneFormat).Output()
	if err != nil {
		return nil
	}
	return parsePanes(string(out))
}

func parsePanes(out string) map[string]Pane {
	panes := map[string]Pane{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 8 {
			continue
		}
		p := Pane{ID: f[0], Session: f[1], WindowID: f[2], WindowIndex: f[3], WindowName: f[4], PaneIndex: f[5], Visible: f[6] == "1"}
		p.SeenAt = parseInt64(f[7]) * 1000
		panes[p.ID] = p
	}
	return panes
}

// Capture returns the last lines of a pane with ANSI styling intact.
func Capture(paneID string, lines int) string {
	if lines < 1 {
		lines = 1
	}
	out, err := exec.Command("tmux", "capture-pane", "-p", "-e", "-J", "-t", paneID, "-S", "-"+strconv.Itoa(lines)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n ")
}

// Jump focuses paneID on client (a tty from #{client_tty}; empty means the current client).
func Jump(paneID, client string) error {
	sw := []string{"switch-client", "-t", paneID}
	if client != "" {
		sw = []string{"switch-client", "-c", client, "-t", paneID}
	}
	if err := exec.Command("tmux", sw...).Run(); err != nil {
		return err
	}
	if err := exec.Command("tmux", "select-window", "-t", paneID).Run(); err != nil {
		return err
	}
	return exec.Command("tmux", "select-pane", "-t", paneID).Run()
}

// InTmux reports whether this process runs inside tmux.
func InTmux() bool { return os.Getenv("TMUX") != "" }

func parseInt64(s string) int64 {
	var n int64
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}

// ClientTheme returns tmux's light/dark detection for client (or the current client),
// or "" when tmux doesn't know (older tmux, or a terminal that doesn't report it).
func ClientTheme(client string) string {
	args := []string{"display-message", "-p"}
	if client != "" {
		args = append(args, "-c", client)
	}
	out, err := exec.Command("tmux", append(args, "#{client_theme}")...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
