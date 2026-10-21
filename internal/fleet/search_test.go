package fleet

import (
	"testing"
	"time"
)

const searchFixture = `{"type":"user","timestamp":"2026-10-20T09:00:00Z","message":{"content":"Ask Ada about the billing rollout before Friday"}}
{"type":"assistant","timestamp":"2026-10-20T09:00:05Z","message":{"content":[{"type":"text","text":"I'll draft a note to ada."},{"type":"tool_use","id":"t1","name":"Grep","input":{"pattern":"billing"}}]}}
{"type":"user","timestamp":"2026-10-20T09:00:06Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"src/ada.go:3: owner"}]}]}}
{"type":"user","isMeta":true,"timestamp":"2026-10-20T09:00:07Z","message":{"content":"skill text that mentions zebra"}}
`

func indexed(t *testing.T) *transcriptTail {
	p := write(t, t.TempDir(), "s.jsonl", searchFixture)
	tt := newTranscriptTail(p, true)
	if err := tt.Refresh(time.Now()); err != nil {
		t.Fatal(err)
	}
	return tt
}

func TestSearchPrefersYourPrompt(t *testing.T) {
	h, ok := indexed(t).corpus.search([]string{"ada"})
	if !ok {
		t.Fatal("no hit")
	}
	if h.Count != 3 || h.Source != FromYou {
		t.Errorf("count %d source %v, want 3 from you", h.Count, h.Source)
	}
	if h.Snippet != "Ask Ada about the billing rollout before Friday" {
		t.Errorf("snippet %q", h.Snippet)
	}
}

func TestSearchTermsAndSources(t *testing.T) {
	c := indexed(t).corpus
	h, ok := c.search([]string{"nobody", "draft"})
	if !ok || h.Terms[0] || !h.Terms[1] || h.Source != FromClaude {
		t.Errorf("hit %+v ok %v", h, ok)
	}
	if h, _ := c.search([]string{"zebra"}); h.Source != FromTool {
		t.Errorf("isMeta text attributed to %v, want tool", h.Source)
	}
	if _, ok := c.search([]string{"absent"}); ok {
		t.Error("absent term matched")
	}
}

func TestSnippetTrimsLongText(t *testing.T) {
	var c corpus
	long := "start " + string(make([]byte, 0)) + "aaaaaaaaaa bbbbbbbbbb cccccccccc dddddddddd eeeeeeeeee NEEDLE ffffffffff gggggggggg hhhhhhhhhh iiiiiiiiii jjjjjjjjjj kkkkkkkkkk"
	c.add(FromYou, time.Now(), long)
	h, _ := c.search([]string{"needle"})
	if len(h.Snippet) > 130 || h.Snippet[:3] != "…" {
		t.Errorf("snippet %q", h.Snippet)
	}
}
