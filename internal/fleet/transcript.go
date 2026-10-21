package fleet

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Edit is one file write seen in a transcript.
type Edit struct {
	Path string
	At   time.Time
}

// TranscriptInfo is what the board shows from a session's transcript.
type TranscriptInfo struct {
	Title       string    `json:"title,omitempty"`
	LastPrompt  string    `json:"lastPrompt,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	Model       string    `json:"model,omitempty"`
	Tool        string    `json:"tool,omitempty"`     // tool of the last tool_use, if still open
	Action      string    `json:"action,omitempty"`   // one-line summary of the last tool_use
	LastText    string    `json:"lastText,omitempty"` // first line of the last assistant text
	Tokens      int       `json:"contextTokens"`      // context size of the last main-thread request
	LastEventAt time.Time `json:"lastEventAt,omitempty"`
	Edits       []Edit    `json:"-"`
}

// transcriptTail incrementally reads a .jsonl transcript by byte offset.
type transcriptTail struct {
	path    string
	offset  int64
	mtime   time.Time
	size    int64
	info    TranscriptInfo
	openUse map[string]bool // tool_use ids without a result yet
	index   bool            // collect searchable text into corpus
	corpus  corpus
}

const editWindow = 30 * time.Minute

func newTranscriptTail(path string, index bool) *transcriptTail {
	return &transcriptTail{path: path, openUse: map[string]bool{}, index: index}
}

// findTranscript locates <sessionId>.jsonl under projectsDir. The directory name is a
// lossy encoding of cwd, so glob instead of reconstructing it.
func findTranscript(projectsDir, sessionID string) string {
	m, _ := filepath.Glob(filepath.Join(projectsDir, "*", sessionID+".jsonl"))
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

// Refresh reads any bytes appended since the last call. Cheap when nothing changed.
func (t *transcriptTail) Refresh(now time.Time) error {
	st, err := os.Stat(t.path)
	if err != nil {
		return err
	}
	if st.ModTime().Equal(t.mtime) && st.Size() == t.size {
		return nil
	}
	if st.Size() < t.offset { // rewritten; start over
		*t = *newTranscriptTail(t.path, t.index)
	}
	f, err := os.Open(t.path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(f, 256*1024)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break // EOF or partial last line: leave it for next time
		}
		t.offset += int64(len(line))
		t.consume(line)
	}
	t.mtime, t.size = st.ModTime(), st.Size()
	t.pruneEdits(now)
	return nil
}

// Line types worth decoding. Everything else (api-request blobs can be megabytes) is skipped.
var interesting = [][]byte{
	[]byte(`"type":"assistant"`),
	[]byte(`"type":"user"`),
	[]byte(`"type":"ai-title"`),
	[]byte(`"type":"last-prompt"`),
}

type rawLine struct {
	Type        string    `json:"type"`
	AITitle     string    `json:"aiTitle"`
	LastPrompt  string    `json:"lastPrompt"`
	GitBranch   string    `json:"gitBranch"`
	IsSidechain bool      `json:"isSidechain"`
	IsMeta      bool      `json:"isMeta"` // injected by the harness, not typed by you
	Timestamp   time.Time `json:"timestamp"`
	Message     struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			Input         int `json:"input_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type rawBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Text      string          `json:"text"`
	ToolUseID string          `json:"tool_use_id"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
}

func (t *transcriptTail) consume(line []byte) {
	ok := false
	for _, m := range interesting {
		if bytes.Contains(line, m) {
			ok = true
			break
		}
	}
	if !ok {
		return
	}
	var l rawLine
	if json.Unmarshal(line, &l) != nil {
		return
	}
	in := &t.info
	switch l.Type {
	case "ai-title":
		in.Title = l.AITitle
		return
	case "last-prompt":
		in.LastPrompt = firstLine(l.LastPrompt)
		return
	}
	if l.IsSidechain {
		return
	}
	if l.GitBranch != "" {
		in.Branch = l.GitBranch
	}
	if !l.Timestamp.IsZero() {
		in.LastEventAt = l.Timestamp
	}
	var blocks []rawBlock
	var plain string
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		_ = json.Unmarshal(l.Message.Content, &plain)
	}

	if l.Type == "user" {
		you := FromYou
		if l.IsMeta {
			you = FromTool
		}
		if t.index {
			t.corpus.add(you, l.Timestamp, plain)
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				delete(t.openUse, b.ToolUseID)
				if t.index {
					t.corpus.add(FromTool, l.Timestamp, textOf(b.Content))
				}
			case "text":
				if t.index {
					t.corpus.add(you, l.Timestamp, b.Text)
				}
			}
		}
		if len(t.openUse) == 0 {
			in.Tool = ""
		}
		return
	}

	// assistant
	if l.Message.Model != "" {
		in.Model = l.Message.Model
	}
	if u := l.Message.Usage; u != nil {
		if n := u.Input + u.CacheRead + u.CacheCreation; n > 0 {
			in.Tokens = n
		}
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if s := firstLine(b.Text); s != "" {
				in.LastText = s
			}
			if t.index {
				t.corpus.add(FromClaude, l.Timestamp, b.Text)
			}
		case "tool_use":
			if t.index {
				t.corpus.add(FromTool, l.Timestamp, string(b.Input))
			}
			t.openUse[b.ID] = true
			in.Tool = b.Name
			in.Action = summarizeTool(b.Name, b.Input)
			if p := editedPath(b.Name, b.Input); p != "" {
				in.Edits = append(in.Edits, Edit{Path: p, At: l.Timestamp})
			}
		}
	}
}

func (t *transcriptTail) pruneEdits(now time.Time) {
	e := t.info.Edits[:0]
	for _, x := range t.info.Edits {
		if now.Sub(x.At) <= editWindow {
			e = append(e, x)
		}
	}
	t.info.Edits = e
}

type toolInput struct {
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	Command      string `json:"command"`
	Description  string `json:"description"`
	Pattern      string `json:"pattern"`
	Query        string `json:"query"`
	URL          string `json:"url"`
	Skill        string `json:"skill"`
}

func editedPath(tool string, raw json.RawMessage) string {
	switch tool {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
	default:
		return ""
	}
	var in toolInput
	_ = json.Unmarshal(raw, &in)
	if in.FilePath != "" {
		return in.FilePath
	}
	return in.NotebookPath
}

func summarizeTool(tool string, raw json.RawMessage) string {
	var in toolInput
	_ = json.Unmarshal(raw, &in)
	arg := ""
	switch {
	case in.FilePath != "":
		arg = filepath.Base(in.FilePath)
	case in.NotebookPath != "":
		arg = filepath.Base(in.NotebookPath)
	case in.Description != "":
		arg = in.Description
	case in.Command != "":
		arg = in.Command
	case in.Pattern != "":
		arg = in.Pattern
	case in.Query != "":
		arg = in.Query
	case in.URL != "":
		arg = in.URL
	case in.Skill != "":
		arg = in.Skill
	}
	arg = firstLine(arg)
	if arg == "" {
		return tool
	}
	return tool + " · " + arg
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
