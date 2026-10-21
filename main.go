// Command ccfleet shows every Claude Code session running in tmux and which ones need you.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jdbrewer/ccfleet/internal/fleet"
	"github.com/jdbrewer/ccfleet/internal/tui"
)

const usage = `ccfleet: a board for your Claude Code sessions

usage:
  ccfleet [--client TTY]   open the board (vim keys; ? for help)
  ccfleet ls [--json]      print a snapshot
  ccfleet grep TERMS...    find sessions whose transcript or metadata mention all TERMS
  ccfleet badge            short needs-you summary for tmux status-right
  ccfleet tmux-snippet     print tmux.conf lines: popup binding, badge, focus hook
`

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "":
		err = runBoard(args)
	case "ls":
		err = runLs(args)
	case "grep":
		err = runGrep(args)
	case "badge":
		err = runBadge()
	case "tmux-snippet":
		os.Stdout.WriteString(tmuxSnippet)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ccfleet:", err)
		os.Exit(1)
	}
}

func runBoard(args []string) error {
	fs := flag.NewFlagSet("ccfleet", flag.ExitOnError)
	client := fs.String("client", "", "tmux client tty to jump on (pass #{client_tty} from a popup)")
	fs.Usage = func() { fmt.Print(usage) }
	fs.Parse(args)
	return tui.Run(fleet.New(fleet.Config{Index: true}), *client)
}

func runLs(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	ss, err := fleet.New(fleet.Config{}).Snapshot()
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(ss)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "STATE\tFOR\tPANE\tNAME\tREPO\tBRANCH\tCTX\tDOING\tTASK")
	for _, s := range ss {
		doing := s.Info.Action
		if s.State != fleet.Working {
			doing = ""
		}
		flag := ""
		if len(s.Collisions) > 0 {
			flag = " ⚠"
		}
		fmt.Fprintf(w, "%s%s\t%s\t%s\t%s\t%s\t%s\t%d%%\t%s\t%s\n", s.State, flag, Ago(s.StateSince), s.Tmux.Pane, s.Name,
			s.Repo, s.Info.Branch, s.ContextPct(), clip(doing, 40), clip(s.Label(), 60))
	}
	return w.Flush()
}

func runGrep(args []string) error {
	fs := flag.NewFlagSet("grep", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Parse(args)
	query := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("usage: ccfleet grep [--json] TERMS...")
	}
	f := fleet.New(fleet.Config{Index: true})
	ss, err := f.Snapshot()
	if err != nil {
		return err
	}
	hits := f.Search(query)
	type match struct {
		fleet.Session
		Hit *fleet.Hit `json:"hit,omitempty"`
	}
	var out []match
	for _, s := range ss {
		var h *fleet.Hit
		if x, ok := hits[s.ID]; ok {
			h = &x
		}
		if fleet.Matches(s, query, h) {
			out = append(out, match{s, h})
		}
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	if len(out) == 0 {
		fmt.Fprintf(os.Stderr, "no live session mentions %q\n", query)
		os.Exit(1)
	}
	for _, m := range out {
		fmt.Printf("%s  %s  %s  %s\n", m.State, m.Tmux.Pane, m.Name, clip(m.Label(), 60))
		if m.Hit != nil {
			fmt.Printf("    %d hits · %s, %s ago: “%s”\n", m.Hit.Count, m.Hit.Source, Ago(m.Hit.At), m.Hit.Snippet)
		}
	}
	return nil
}

func runBadge() error {
	ss, err := fleet.New(fleet.Config{}).States()
	if err != nil {
		return err
	}
	var wait, done, work int
	for _, s := range ss {
		switch s.State {
		case fleet.Waiting:
			wait++
		case fleet.Done:
			done++
		case fleet.Working:
			work++
		}
	}
	var parts []string
	if wait > 0 {
		parts = append(parts, fmt.Sprintf("#[fg=yellow,bold]⏸%d#[default]", wait))
	}
	if done > 0 {
		parts = append(parts, fmt.Sprintf("#[fg=green]✓%d#[default]", done))
	}
	parts = append(parts, fmt.Sprintf("#[fg=colour244]●%d#[default]", work))
	fmt.Println(strings.Join(parts, " "))
	return nil
}

// Ago renders a compact age like 4s, 12m, 3h, 2d.
func Ago(t time.Time) string { return fleet.Ago(t, time.Now()) }

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

const tmuxSnippet = `# ccfleet: board in a popup on prefix+a, Enter jumps to the pane.
# run-shell is needed: display-popup does not expand #{client_tty} in its command.
bind-key a run-shell -b "tmux display-popup -c '#{client_tty}' -E -w 92% -h 85% 'ccfleet --client #{client_tty}'"

# needs-you badge in the status line (⏸ waiting on you · ✓ finished, unseen · ● working).
# The badge refreshes every status-interval; 5s is snappy. Remove this line to keep yours.
set -g status-interval 5
set -ag status-right ' #(ccfleet badge)'

# optional: count a pane as seen whenever you focus it, even outside ccfleet
set -g focus-events on
set-hook -ga pane-focus-in 'run-shell -b "tmux set -p -t #{pane_id} @ccfleet_seen $(date +%s)"'
`
