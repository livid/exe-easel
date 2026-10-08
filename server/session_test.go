package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureStudio = "/www/exe-art/studios/haixing-2"

func parseFixture(t *testing.T) *Session {
	t.Helper()
	s := NewSession("testdata/session.jsonl", fixtureStudio)
	if _, err := s.Update(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSessionFixture(t *testing.T) {
	s := parseFixture(t)
	events, next, _ := s.Events(0)
	if len(events) == 0 || next != len(events) {
		t.Fatalf("events %d next %d", len(events), next)
	}
	if events[0]["kind"] != "start" || events[0]["model"] != "claude-opus-5-5" || events[0]["t"].(int64) == 0 {
		t.Fatalf("first event %v", events[0])
	}
	kinds := map[string]int{}
	for i, e := range events {
		kinds[e["kind"].(string)]++
		if e["seq"].(int) != i+1 {
			t.Fatalf("seq %v at %d", e["seq"], i)
		}
	}
	for _, k := range []string{"think", "read", "paint", "look", "note", "say"} {
		if kinds[k] == 0 {
			t.Errorf("no %s events: %v", k, kinds)
		}
	}
	if s.Looks != kinds["look"] {
		t.Errorf("Looks %d, look events %d", s.Looks, kinds["look"])
	}
	for _, e := range events {
		if e["kind"] != "look" || e["error"] != nil {
			continue
		}
		imgs := e["images"].([]string)
		if len(imgs) == 0 {
			t.Fatalf("look without images: %v", e)
		}
		for _, p := range imgs {
			if !strings.HasPrefix(p, "out/easel/") || !strings.HasSuffix(p, ".png") {
				t.Errorf("look path %q", p)
			}
		}
		if e["w"].(int) == 0 || e["h"].(int) == 0 {
			t.Errorf("look size %v", e)
		}
		if strings.Contains(e["said"].(string), fixtureStudio) {
			t.Errorf("said keeps the studio's path: %q", e["said"])
		}
	}
	b, _ := json.Marshal(events)
	if strings.Contains(string(b), "iVBOR") || strings.Contains(string(b), "/9j/") || strings.Contains(string(b), "[Image: source") {
		t.Error("an image payload or Claude Code's image line reached the events")
	}
	if s.Clock == "" || !strings.HasPrefix(s.Clock, "day ") {
		t.Errorf("clock %q", s.Clock)
	}
	if p, w, h := s.LatestLook(); p == "" || w == 0 || h == 0 {
		t.Errorf("latest %q %d×%d", p, w, h)
	}
	if s.Tokens.Output == 0 || s.Tokens.CacheRead == 0 {
		t.Errorf("tokens %+v", s.Tokens)
	}
	after, _, _ := s.Events(next - 3)
	if len(after) != 3 || after[0]["seq"].(int) != next-2 {
		t.Errorf("after: %d events", len(after))
	}
}

// Fed a line at a time, with a partial line in between, the parse comes out
// the same as all at once.
func TestSessionIncremental(t *testing.T) {
	whole := parseFixture(t)
	want, _, _ := whole.Events(0)
	raw, _ := os.ReadFile("testdata/session.jsonl")
	path := filepath.Join(t.TempDir(), "session.jsonl")
	s := NewSession(path, fixtureStudio)
	f, _ := os.Create(path)
	lines := strings.SplitAfter(string(raw), "\n")
	for i, ln := range lines {
		if ln == "" {
			continue
		}
		half := len(ln) / 2
		f.WriteString(ln[:half])
		f.Sync()
		s.Update()
		if _, n, _ := s.Events(0); i > 0 && n == 0 && i > 5 {
			t.Fatalf("nothing parsed after %d lines", i)
		}
		f.WriteString(ln[half:])
		f.Sync()
		s.Update()
	}
	f.Close()
	got, _, _ := s.Events(0)
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(got)
	if string(a) != string(b) {
		t.Fatalf("incremental parse differs: %d vs %d events", len(want), len(got))
	}
	if whole.Tokens != s.Tokens || whole.Looks != s.Looks || whole.Clock != s.Clock {
		t.Fatalf("figures differ: %+v %+v", whole.Tokens, s.Tokens)
	}
}

// A call with no result yet is pending; the result makes it an event.
func TestSessionPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	call := `{"type":"assistant","timestamp":"2026-10-08T07:32:18.603Z","message":{"id":"m1","usage":{"input_tokens":3,"output_tokens":5},"content":[{"type":"tool_use","id":"tu1","name":"mcp__easel__paint","input":{"lua":"print(1)"}}]}}` + "\n"
	os.WriteFile(path, []byte(call), 0o644)
	s := NewSession(path, "/s")
	s.Update()
	ev, _, pending := s.Events(0)
	if len(ev) != 0 || pending == nil || pending["kind"] != "paint" || pending["lua"] != "print(1)" {
		t.Fatalf("events %v pending %v", ev, pending)
	}
	res := `{"type":"user","timestamp":"2026-10-08T07:32:19Z","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","content":[{"type":"text","text":"1\nday 1, 09:30\nok"},{"type":"image","source":{"type":"base64","data":"iVBORw0KGgo"}}]}]}}` + "\n"
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(res)
	f.WriteString(`{"type":"result","result":"Done.","total_cost_usd":1.25,"num_turns":4,"usage":{"input_tokens":10,"output_tokens":900,"cache_read_input_tokens":7,"cache_creation_input_tokens":2}}` + "\n")
	f.Close()
	s.Update()
	ev, next, pending := s.Events(0)
	if pending != nil || len(ev) != 2 || next != 2 {
		t.Fatalf("events %v pending %v", ev, pending)
	}
	if ev[0]["reply"] != "1\nday 1, 09:30\nok" || s.Clock != "day 1, 09:30" {
		t.Fatalf("paint %v clock %q", ev[0], s.Clock)
	}
	if ev[1]["kind"] != "result" || ev[1]["cost_usd"] != 1.25 || s.Cost != 1.25 {
		t.Fatalf("result %v", ev[1])
	}
	if s.Tokens != (Tokens{10, 900, 7, 2}) {
		t.Fatalf("tokens after the run: %+v (the result's usage replaces the streamed figures)", s.Tokens)
	}
	// a replaced file starts over
	os.WriteFile(path, []byte(call), 0o644)
	s.Update()
	if ev, n, p := s.Events(0); len(ev) != 0 || n != 0 || p == nil {
		t.Fatalf("after replace: %d events, pending %v", len(ev), p)
	}
}

func TestTitleFrom(t *testing.T) {
	cases := map[string]string{
		"**Title:** Pilot, One Stroke of Evening\n\nThe look showed…": "Pilot, One Stroke of Evening",
		"# *Low Tide, Counting*\nA girl.":                             "Low Tide, Counting",
		"\n\n“The Fridge Light”\n":                                    "The Fridge Light",
		"Title — 《冰箱的光》":                                              "冰箱的光",
		"_Headland at Dusk_":                                          "Headland at Dusk",
		"":                                                            "",
		strings.Repeat("long ", 40):                                   "",
		"**\"Two Weeks\"** — painted over a day":                      "\"Two Weeks\" — painted over a day",
		"**《冰箱的光》 (In the Light of the Refrigerator)**\n\nA night kitchen.":                              "冰箱的光",
		"I've finished the painting for chapter 5. It's called 《手指画的星》, \"The Star Drawn by a Finger\".": "手指画的星",
		"**「石下的眼睛」(The Eye Under the Stone)**":                                                           "石下的眼睛",
		"负潮 · Minus Tide\n\nThe cliff is called 《not this》 later.":                                       "负潮 · Minus Tide",
	}
	for in, want := range cases {
		if got := titleFrom(in); got != want {
			t.Errorf("titleFrom(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRuns(t *testing.T) {
	p := filepath.Join(t.TempDir(), "runs.log")
	os.WriteFile(p, []byte("2026-10-08T07:32:16Z start model=claude-opus-5-5 effort=high hours=5 \n2026-10-08T09:00:00Z end status=0\n2026-10-08T10:00:00Z start model=claude-haiku-5-5 effort=low hours=1 --resume abc\n"), 0o644)
	ri := readRuns(p)
	if ri.model != "claude-haiku-5-5" || ri.effort != "low" || ri.started == nil || ri.ended != nil || ri.exit != nil || len(ri.lines) != 3 {
		t.Fatalf("%+v", ri)
	}
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("2026-10-08T10:30:00Z end status=130\n")
	f.Close()
	ri = readRuns(p)
	if ri.ended == nil || ri.exit == nil || *ri.exit != 130 {
		t.Fatalf("%+v", ri)
	}
}
