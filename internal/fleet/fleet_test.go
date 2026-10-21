package fleet

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseTmuxTarget(t *testing.T) {
	cases := map[string]TmuxTarget{
		"1:@36.%147":     {Session: "1", Window: "@36", Pane: "%147"},
		"work:dev:@2.%9": {Session: "work:dev", Window: "@2", Pane: "%9"},
		"":               {},
		"garbage":        {},
		"1:@36.notapane": {},
	}
	for in, want := range cases {
		if got := ParseTmuxTarget(in); got != want {
			t.Errorf("ParseTmuxTarget(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestDeriveState(t *testing.T) {
	t0 := time.UnixMilli(1_000_000)
	before, after := t0.Add(-time.Minute), t0.Add(time.Minute)
	cases := []struct {
		status string
		seen   time.Time
		want   State
	}{
		{"waiting", after, Waiting},
		{"busy", before, Working},
		{"shell", before, Working},
		{"idle", before, Done},      // went idle after last look
		{"idle", after, Idle},       // looked after it went idle
		{"idle", time.Time{}, Done}, // never seen
		{"mystery", before, Idle},
	}
	for _, c := range cases {
		if got := deriveState(c.status, t0, c.seen); got != c.want {
			t.Errorf("deriveState(%s, seen %v) = %v, want %v", c.status, c.seen, got, c.want)
		}
	}
}

func TestLoadSessionsSkipsDeadAndNonJSON(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "100.json", `{"pid":100,"sessionId":"a","status":"busy","tmux":"1:@1.%1"}`)
	write(t, dir, "200.json", `{"pid":200,"sessionId":"b","status":"idle"}`)
	write(t, dir, "100.abc.key", `secret`)
	write(t, dir, "300.json", `{not json`)
	got, parsed, err := loadSessions(dir, func([]rawSession) map[int]bool { return map[int]bool{100: true} })
	if err != nil || !parsed {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "a" {
		t.Fatalf("got %+v, want only session a", got)
	}
}

const transcriptFixture = `{"type":"user","gitBranch":"main","timestamp":"2026-10-20T10:00:00Z","message":{"role":"user","content":"build the thing"}}
{"type":"api-request-blob","message":"huge"}
{"type":"assistant","gitBranch":"feat/x","timestamp":"2026-10-20T10:00:05Z","message":{"model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_read_input_tokens":80000,"cache_creation_input_tokens":1000},"content":[{"type":"text","text":"On it.\nmore"},{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/repo/src/a.go"}}]}}
{"type":"assistant","isSidechain":true,"message":{"usage":{"input_tokens":999999},"content":[]}}
{"type":"ai-title","aiTitle":"Build the thing"}
{"type":"last-prompt","lastPrompt":"build the thing\nplease"}
`

func TestTranscriptTail(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "s.jsonl", transcriptFixture)
	now := time.Date(2026, 10, 20, 10, 5, 0, 0, time.UTC)
	tt := newTranscriptTail(p, false)
	if err := tt.Refresh(now); err != nil {
		t.Fatal(err)
	}
	in := tt.info
	if in.Title != "Build the thing" || in.LastPrompt != "build the thing" || in.Branch != "feat/x" {
		t.Errorf("title/prompt/branch = %q/%q/%q", in.Title, in.LastPrompt, in.Branch)
	}
	if in.Tokens != 81002 {
		t.Errorf("tokens = %d, want 81002 (sidechain usage must be ignored)", in.Tokens)
	}
	if in.Tool != "Edit" || in.Action != "Edit · a.go" || in.LastText != "On it." {
		t.Errorf("tool/action/text = %q/%q/%q", in.Tool, in.Action, in.LastText)
	}
	if len(in.Edits) != 1 || in.Edits[0].Path != "/repo/src/a.go" {
		t.Errorf("edits = %+v", in.Edits)
	}

	// append a tool result and a partial line: tool closes, partial waits
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1"}]}}` + "\n" + `{"type":"ai-title","aiT`)
	f.Close()
	if err := tt.Refresh(now); err != nil {
		t.Fatal(err)
	}
	if tt.info.Tool != "" {
		t.Errorf("tool still open after result: %q", tt.info.Tool)
	}
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`itle":"Renamed"}` + "\n")
	f.Close()
	_ = tt.Refresh(now)
	if tt.info.Title != "Renamed" {
		t.Errorf("partial line not completed: title %q", tt.info.Title)
	}

	// edits age out
	_ = tt.Refresh(now) // no change, no-op
	tt.pruneEdits(now.Add(time.Hour))
	if len(tt.info.Edits) != 0 {
		t.Errorf("old edits kept: %+v", tt.info.Edits)
	}
}

func TestFindCollisions(t *testing.T) {
	ss := []Session{
		{Name: "a", RepoRoot: "/r", State: Working, Info: TranscriptInfo{Branch: "main", Edits: []Edit{{Path: "/r/x.go"}, {Path: "/r/x.go"}}}},
		{Name: "b", RepoRoot: "/r", State: Working, Info: TranscriptInfo{Branch: "main", Edits: []Edit{{Path: "/r/x.go"}}}},
		{Name: "c", RepoRoot: "/r-wt", State: Working, Info: TranscriptInfo{Branch: "main", Edits: []Edit{{Path: "/r-wt/x.go"}}}},
		{Name: "d", RepoRoot: "/r", State: Idle, Info: TranscriptInfo{Branch: "main"}},
	}
	findCollisions(ss)
	want := []string{"branch main also active in b", "file x.go also edited by b"}
	if strings.Join(ss[0].Collisions, "|") != strings.Join(want, "|") {
		t.Errorf("a collisions = %q", ss[0].Collisions)
	}
	if len(ss[2].Collisions) != 0 || len(ss[3].Collisions) != 0 {
		t.Errorf("worktree or idle session flagged: %q %q", ss[2].Collisions, ss[3].Collisions)
	}
}

func TestSortSessions(t *testing.T) {
	t0 := time.Now()
	ss := []Session{
		{Name: "idle", State: Idle, StateSince: t0},
		{Name: "done-new", State: Done, StateSince: t0},
		{Name: "work", State: Working, StateSince: t0},
		{Name: "done-old", State: Done, StateSince: t0.Add(-time.Hour)},
		{Name: "wait", State: Waiting, StateSince: t0},
	}
	SortSessions(ss)
	var got []string
	for _, s := range ss {
		got = append(got, s.Name)
	}
	if strings.Join(got, ",") != "wait,done-old,done-new,work,idle" {
		t.Errorf("order = %v", got)
	}
}

func TestSnapshotEndToEnd(t *testing.T) {
	claude := t.TempDir()
	now := time.Now()
	idleAt := now.Add(-10 * time.Minute).UnixMilli()
	write(t, filepath.Join(claude, "sessions"), "1.json",
		`{"pid":1,"sessionId":"s1","name":"one","cwd":"/nowhere","status":"idle","statusUpdatedAt":`+strconv.FormatInt(idleAt, 10)+`,"tmux":"w:@1.%5"}`)
	write(t, filepath.Join(claude, "projects", "-nowhere"), "s1.jsonl", transcriptFixture)
	write(t, filepath.Join(claude, "teams", "t"), "config.json",
		`{"name":"squad","leadSessionId":"zzz","members":[{"name":"team-lead","agentType":"team-lead"},{"name":"tester","agentType":"general-purpose","tmuxPaneId":"%5"}]}`)

	stateFile := filepath.Join(t.TempDir(), "seen.json")
	write(t, filepath.Dir(stateFile), "seen.json", `{}`) // not a first run
	f := New(Config{ClaudeDir: claude, StateFile: stateFile})
	f.alive = func(rs []rawSession) map[int]bool {
		m := map[int]bool{}
		for _, r := range rs {
			m[r.PID] = true
		}
		return m
	}
	f.panes = func() map[string]Pane {
		return map[string]Pane{"%5": {ID: "%5", WindowIndex: "2", WindowName: "api"}}
	}
	ss, err := f.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 {
		t.Fatalf("got %d sessions", len(ss))
	}
	s := ss[0]
	if s.State != Done || s.Window != "2:api" || s.Team != "squad" || s.Role != "tester" || s.Label() != "Build the thing" {
		t.Errorf("session = %+v", s)
	}
	f.MarkSeen("s1")
	ss, _ = f.Snapshot()
	if ss[0].State != Idle {
		t.Errorf("after MarkSeen state = %v, want idle", ss[0].State)
	}
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDefaultProcAliveRejectsReusedPid(t *testing.T) {
	self := os.Getpid()
	start, ok := procStart(self)
	if !ok || start.IsZero() {
		t.Skip("no process start time on this platform")
	}
	ms := start.UnixMilli()
	got := defaultProcAlive([]rawSession{
		{PID: self, StartedAt: ms + 1000},                       // session began after process: alive
		{PID: self, StartedAt: ms - 2*time.Hour.Milliseconds()}, // process started long after: reused pid
		{PID: 999999, StartedAt: ms},                            // not running
	})
	if !got[self] {
		t.Error("own process not alive")
	}
	if got[999999] {
		t.Error("dead pid alive")
	}
	if got2 := defaultProcAlive([]rawSession{{PID: self, StartedAt: ms - 2*time.Hour.Milliseconds()}}); got2[self] {
		t.Error("reused pid counted alive")
	}
}

func TestSeenSurvivesEmptyRead(t *testing.T) {
	now := time.Now()
	s := loadSeen(filepath.Join(t.TempDir(), "seen.json"))
	s.mark("recent", now.Add(-time.Hour))
	s.mark("old", now.Add(-48*time.Hour))
	s.prune(map[string]bool{}, now)
	if s.at("recent").IsZero() {
		t.Error("recent mark dropped by an empty read")
	}
	if !s.at("old").IsZero() {
		t.Error("stale mark kept")
	}
}

func TestLoadSessionsDetectsFormatChange(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "1.json", `{"processId":1,"id":"renamed fields"}`)
	if _, parsed, _ := loadSessions(dir, func([]rawSession) map[int]bool { return nil }); parsed {
		t.Error("unrecognised session files reported as parsed; the CLI fallback would never run")
	}
	empty := t.TempDir()
	if _, parsed, _ := loadSessions(empty, func([]rawSession) map[int]bool { return nil }); !parsed {
		t.Error("an empty sessions dir is not a format change")
	}
}
