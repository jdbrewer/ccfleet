package fleet

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Source says who wrote the text a search hit came from.
type Source byte

const (
	FromYou    Source = 'u' // your prompts
	FromClaude Source = 'c' // assistant replies
	FromTool   Source = 't' // tool inputs and results
)

func (s Source) String() string {
	switch s {
	case FromYou:
		return "you"
	case FromClaude:
		return "claude"
	}
	return "tool"
}

// rank orders sources for picking the snippet: your own words are what you remember.
func (s Source) rank() int { return strings.IndexByte("uct", byte(s)) }

// Hit is one session's match for a search.
type Hit struct {
	Terms   []bool    `json:"-"`     // which query terms appear in the transcript
	Count   int       `json:"count"` // occurrences of the snippet's term
	Source  Source    `json:"-"`
	At      time.Time `json:"at"`
	Snippet string    `json:"snippet"`
	Term    string    `json:"term"`
}

func (h Hit) MarshalJSON() ([]byte, error) {
	type alias Hit
	return json.Marshal(struct {
		alias
		From string `json:"from"`
	}{alias(h), h.Source.String()})
}

// tool results can be whole files; index only their head
const maxToolText = 16 << 10

// corpus is a session's searchable text. lower is an ASCII-lowercased copy of text so
// byte offsets line up between the two.
type corpus struct {
	text, lower []byte
	chunks      []chunk
}

type chunk struct {
	off int
	src Source
	at  time.Time
}

func (c *corpus) add(src Source, at time.Time, s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if src == FromTool && len(s) > maxToolText {
		s = s[:maxToolText]
	}
	c.chunks = append(c.chunks, chunk{off: len(c.text), src: src, at: at})
	c.text = append(c.text, s...)
	c.text = append(c.text, '\n')
	start := len(c.lower)
	c.lower = append(c.lower, s...)
	c.lower = append(c.lower, '\n')
	asciiLower(c.lower[start:])
}

func asciiLower(b []byte) {
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 32
		}
	}
}

// chunkAt returns the chunk containing byte offset off.
func (c *corpus) chunkAt(off int) (chunk, int) {
	i := sort.Search(len(c.chunks), func(i int) bool { return c.chunks[i].off > off }) - 1
	end := len(c.text)
	if i+1 < len(c.chunks) {
		end = c.chunks[i+1].off
	}
	return c.chunks[i], end
}

// search reports which terms (already lowercased) occur, and a snippet for the best
// occurrence of the first term found: your prompts over replies over tools, then newest.
func (c *corpus) search(terms []string) (Hit, bool) {
	h := Hit{Terms: make([]bool, len(terms))}
	any := false
	for i, t := range terms {
		h.Terms[i] = t != "" && bytes.Contains(c.lower, []byte(t))
		any = any || h.Terms[i]
	}
	if !any {
		return h, false
	}
	for i, t := range terms {
		if !h.Terms[i] {
			continue
		}
		needle := []byte(t)
		h.Count = bytes.Count(c.lower, needle)
		var best chunk
		bestOff := -1
		for off := 0; ; {
			j := bytes.Index(c.lower[off:], needle)
			if j < 0 {
				break
			}
			pos := off + j
			ch, end := c.chunkAt(pos)
			if bestOff < 0 || ch.src.rank() < best.src.rank() || (ch.src == best.src && ch.at.After(best.at)) {
				best, bestOff = ch, pos
			}
			off = max(pos+len(needle), end) // first hit per chunk is enough for the snippet
		}
		h.Source, h.At, h.Term = best.src, best.at, t
		h.Snippet = snippet(c.text, bestOff, len(needle), best.off)
		break
	}
	return h, true
}

// snippet returns one line of context around text[pos:pos+n], not crossing chunk start.
func snippet(text []byte, pos, n, chunkStart int) string {
	const before, after = 40, 80
	s := max(pos-before, chunkStart)
	e := min(pos+n+after, len(text))
	out := string(text[s:e])
	if i := strings.IndexByte(out[pos-s+n:], '\n'); i >= 0 {
		out = out[:pos-s+n+i]
	}
	if i := strings.LastIndexByte(out[:pos-s], '\n'); i >= 0 {
		out, s = out[i+1:], s+i+1
	}
	out = strings.Join(strings.Fields(out), " ")
	if s > chunkStart {
		out = "…" + out
	}
	return out
}

// textOf extracts the readable text of a tool_result's content (string or blocks).
func textOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []rawBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, x := range blocks {
		if x.Type == "text" {
			b.WriteString(x.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// Search matches the given terms against every live session's transcript text.
// Sessions with no term in their transcript are omitted. Requires Config.Index.
func (f *Fleet) Search(query string) map[string]Hit {
	terms := strings.Fields(query)
	for i, t := range terms {
		b := []byte(t)
		asciiLower(b) // same folding as the corpus, so offsets and matches agree
		terms[i] = string(b)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	hits := map[string]Hit{}
	if len(terms) == 0 {
		return hits
	}
	for id, t := range f.tails {
		if h, ok := t.corpus.search(terms); ok {
			hits[id] = h
		}
	}
	return hits
}

// Matches reports whether every query term appears in the session's metadata or, via
// hit, in its transcript. hit may be nil.
func Matches(s Session, query string, hit *Hit) bool {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return true
	}
	hay := strings.ToLower(strings.Join([]string{
		s.Name, s.Repo, s.Info.Branch, s.Label(), s.State.String(), s.Team, s.Role,
		s.Window, s.Tmux.Pane, s.Info.Action, s.Cwd,
	}, " "))
	for i, t := range terms {
		inTranscript := hit != nil && i < len(hit.Terms) && hit.Terms[i]
		if !strings.Contains(hay, t) && !inTranscript {
			return false
		}
	}
	return true
}
