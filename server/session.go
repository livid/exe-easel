package main

// The painter's session: Claude Code's stream-json (out/claude/session.jsonl),
// read incrementally into the events API.md lists. Each tool_use waits in
// `pending` until its tool_result arrives and the pair becomes one event.
// Image payloads (base64) are dropped as they are read: a look is named by
// the path of the PNG the easel wrote.

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Event = map[string]any

type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

type pendingCall struct {
	name  string
	input map[string]any
	t     int64
}

// Session is one studio's parsed transcript; Update reads what was appended.
type Session struct {
	path   string
	studio string // the studio's absolute path, to make look paths relative

	off     int64
	size    int64
	modTime time.Time

	events  []Event
	pending map[string]*pendingCall
	order   []string // pending ids, in call order
	lastT   int64
	msgIDs  map[string]Tokens // each message's usage as last seen (a message's lines repeat it, growing)

	Tokens  Tokens // done + live
	done    Tokens // finished runs: their result lines' usage
	live    Tokens // the run in progress: its assistant lines' usage
	Cost    float64
	Looks   int
	Clock   string
	Latest  string // the newest whole-canvas look of the painting
	LatestW int
	LatestH int
	LatestT int64
	any     string // the newest look of the painting of any kind (not scratch, not palette)
	anyW    int
	anyH    int
}

func NewSession(path, studio string) *Session {
	return &Session{path: path, studio: studio, pending: map[string]*pendingCall{}, msgIDs: map[string]Tokens{}}
}

func (s *Session) reset() {
	*s = *NewSession(s.path, s.studio)
}

// Update reads lines appended since the last call. It reports whether
// anything changed. A file that shrank (replaced) is read from the start.
func (s *Session) Update() (bool, error) {
	fi, err := os.Stat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			if s.off > 0 || len(s.events) > 0 {
				s.reset()
				return true, nil
			}
			return false, nil
		}
		return false, err
	}
	if fi.Size() == s.size && fi.ModTime().Equal(s.modTime) {
		return false, nil
	}
	if fi.Size() < s.off {
		s.reset()
	}
	s.size, s.modTime = fi.Size(), fi.ModTime()
	if fi.Size() == s.off {
		return false, nil
	}
	f, err := os.Open(s.path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := f.Seek(s.off, io.SeekStart); err != nil {
		return false, err
	}
	r := bufio.NewReaderSize(f, 1<<20)
	changed := false
	for {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			// a partial last line waits for the rest of it
			break
		}
		if err != nil {
			return changed, err
		}
		s.off += int64(len(line))
		s.line(line)
		changed = true
	}
	return changed, nil
}

// Events returns the events with seq > after, the highest seq, and the
// call in flight.
func (s *Session) Events(after int) ([]Event, int, Event) {
	out := []Event{}
	for _, e := range s.events {
		if e["seq"].(int) > after {
			out = append(out, e)
		}
	}
	var pending Event
	if len(s.order) > 0 {
		id := s.order[len(s.order)-1]
		if c := s.pending[id]; c != nil {
			pending = s.callEvent(c)
			delete(pending, "seq")
		}
	}
	return out, len(s.events), pending
}

func (s *Session) add(e Event) {
	e["seq"] = len(s.events) + 1
	if _, ok := e["t"]; !ok {
		e["t"] = s.lastT
	}
	s.events = append(s.events, e)
}

type rawLine struct {
	Type      string        `json:"type"`
	Subtype   string        `json:"subtype"`
	Timestamp string        `json:"timestamp"`
	SessionID string        `json:"session_id"`
	Model     string        `json:"model"`
	Message   *rawMessage   `json:"message"`
	ThinkMS   *int64        `json:"thinking_duration_ms"`
	Result    *string       `json:"result"`
	IsError   bool          `json:"is_error"`
	CostUSD   *float64      `json:"total_cost_usd"`
	NumTurns  int           `json:"num_turns"`
	RateLimit *rawRateLimit `json:"rate_limit_info"`
	Usage     *rawUsage     `json:"usage"`
}

type rawUsage struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
}

func add(a, b Tokens) Tokens {
	return Tokens{a.Input + b.Input, a.Output + b.Output, a.CacheRead + b.CacheRead, a.CacheWrite + b.CacheWrite}
}

type rawMessage struct {
	ID      string          `json:"id"`
	Content json.RawMessage `json:"content"`
	Usage   *rawUsage       `json:"usage"`
}

type rawRateLimit struct {
	Status        string `json:"status"`
	RateLimitType string `json:"rateLimitType"`
	ResetsAt      int64  `json:"resetsAt"`
}

type rawBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// one line of the stream; reports whether an event or a figure changed
func (s *Session) line(b []byte) bool {
	var l rawLine
	if err := json.Unmarshal(b, &l); err != nil {
		return false
	}
	if l.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
			if s.lastT == 0 {
				// the events before the first stamped line (system/init) take its time
				for _, e := range s.events {
					if e["t"] == int64(0) {
						e["t"] = t.UnixMilli()
					}
				}
			}
			s.lastT = t.UnixMilli()
		}
	}
	switch l.Type {
	case "system":
		switch l.Subtype {
		case "init":
			s.add(Event{"kind": "start", "model": l.Model, "session": l.SessionID})
			return true
		case "compact_boundary":
			s.add(Event{"kind": "compact"})
			return true
		}
	case "assistant":
		if l.Message == nil {
			return false
		}
		if l.Message.ID != "" && l.Message.Usage != nil {
			// once per message: its largest figures, as its lines repeat them
			u, was := l.Message.Usage, s.msgIDs[l.Message.ID]
			now := Tokens{max(was.Input, u.Input), max(was.Output, u.Output), max(was.CacheRead, u.CacheRead), max(was.CacheWrite, u.CacheWrite)}
			s.live.Input += now.Input - was.Input
			s.live.Output += now.Output - was.Output
			s.live.CacheRead += now.CacheRead - was.CacheRead
			s.live.CacheWrite += now.CacheWrite - was.CacheWrite
			s.msgIDs[l.Message.ID] = now
			s.Tokens = add(s.done, s.live)
		}
		var blocks []rawBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return true
		}
		for _, bl := range blocks {
			switch bl.Type {
			case "text":
				if strings.TrimSpace(bl.Text) != "" {
					s.add(Event{"kind": "say", "text": bl.Text})
				}
			case "thinking", "redacted_thinking":
				e := Event{"kind": "think", "text": bl.Thinking}
				if l.ThinkMS != nil {
					e["ms"] = *l.ThinkMS
				} else {
					e["ms"] = 0
				}
				s.add(e)
			case "tool_use":
				if bl.Input == nil {
					bl.Input = map[string]any{}
				}
				s.pending[bl.ID] = &pendingCall{name: bl.Name, input: bl.Input, t: s.lastT}
				s.order = append(s.order, bl.ID)
			}
		}
		return true
	case "user":
		if l.Message == nil {
			return false
		}
		var blocks []rawBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return false
		}
		changed := false
		for _, bl := range blocks {
			if bl.Type != "tool_result" {
				continue
			}
			c := s.pending[bl.ToolUseID]
			if c == nil {
				continue
			}
			delete(s.pending, bl.ToolUseID)
			for i, id := range s.order {
				if id == bl.ToolUseID {
					s.order = append(s.order[:i], s.order[i+1:]...)
					break
				}
			}
			s.result(c, resultText(bl.Content), bl.IsError)
			changed = true
		}
		return changed
	case "result":
		e := Event{"kind": "result", "turns": l.NumTurns, "cost_usd": 0.0, "text": ""}
		if l.Result != nil {
			e["text"] = *l.Result
		}
		if l.CostUSD != nil {
			e["cost_usd"] = *l.CostUSD
			s.Cost += *l.CostUSD
		}
		if l.IsError {
			e["error"] = e["text"]
		}
		// the run's totals: the stream's assistant lines come before their
		// messages end, so their output counts run low until here
		if u := l.Usage; u != nil {
			s.done = add(s.done, Tokens{u.Input, u.Output, u.CacheRead, u.CacheWrite})
		} else {
			s.done = add(s.done, s.live)
		}
		s.live = Tokens{}
		s.msgIDs = map[string]Tokens{}
		s.Tokens = s.done
		s.add(e)
		return true
	case "rate_limit_event":
		if l.RateLimit != nil && l.RateLimit.Status != "" && l.RateLimit.Status != "allowed" {
			text := l.RateLimit.Status
			if l.RateLimit.RateLimitType != "" {
				text += " · " + l.RateLimit.RateLimitType
			}
			if l.RateLimit.ResetsAt > 0 {
				text += " · resets " + time.Unix(l.RateLimit.ResetsAt, 0).UTC().Format("2006-01-02 15:04 UTC")
			}
			s.add(Event{"kind": "limit", "text": text})
			return true
		}
	}
	return false
}

// resultText is a tool_result's text: its text blocks joined, images left
// out, and Claude Code's "[Image: source: …]" lines with them.
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, p := range parts {
		if p.Type != "text" {
			continue
		}
		var keep []string
		for _, ln := range strings.Split(p.Text, "\n") {
			if strings.HasPrefix(ln, "[Image: source:") || strings.HasPrefix(ln, "[Image:") && strings.Contains(ln, "source:") {
				continue
			}
			keep = append(keep, ln)
		}
		if t := strings.Join(keep, "\n"); strings.TrimSpace(t) != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n")
}

func toolKind(name string) (kind, short string) {
	short = strings.TrimPrefix(name, "mcp__easel__")
	switch short {
	case "paint", "look", "note", "read":
		return short, short
	}
	return "tool", short
}

func str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func boolOf(m map[string]any, k string) bool {
	v, _ := m[k].(bool)
	return v
}

// the event of a call, without its result
func (s *Session) callEvent(c *pendingCall) Event {
	kind, short := toolKind(c.name)
	e := Event{"kind": kind, "t": c.t}
	switch kind {
	case "paint":
		e["lua"] = str(c.input, "lua")
		e["scratch"] = boolOf(c.input, "scratch")
	case "look":
		e["args"] = c.input
	case "note":
		e["text"] = str(c.input, "text")
		e["replaces"] = str(c.input, "replaces")
	case "read":
		e["path"] = str(c.input, "path")
	default:
		e["name"] = short
		e["input"] = c.input
	}
	return e
}

var (
	clockRe = regexp.MustCompile(`day \d+, \d{2}:\d{2}`)
	pngRe   = regexp.MustCompile(`^(.+\.png)(?: \((\d+)x(\d+)[^)]*\))?$`)
)

func (s *Session) result(c *pendingCall, text string, isErr bool) {
	e := s.callEvent(c)
	switch e["kind"] {
	case "paint":
		if isErr {
			e["error"] = text
		} else {
			e["reply"] = text
			if !boolOf(c.input, "scratch") {
				if m := clockRe.FindAllString(text, -1); len(m) > 0 {
					s.Clock = m[len(m)-1]
				}
			}
		}
	case "look":
		s.Looks++
		if isErr {
			e["error"] = text
			e["said"] = ""
			e["images"] = []string{}
			break
		}
		e["said"] = strings.ReplaceAll(text, s.studio+string(filepath.Separator), "")
		images := []string{}
		w, h := 0, 0
		for _, ln := range strings.Split(text, "\n") {
			m := pngRe.FindStringSubmatch(strings.TrimSpace(ln))
			if m == nil {
				continue
			}
			images = append(images, s.rel(m[1]))
			if len(images) == 1 && m[2] != "" {
				w, h = atoi(m[2]), atoi(m[3])
			}
		}
		e["images"] = images
		e["w"], e["h"] = w, h
		in := c.input
		if len(images) > 0 && !boolOf(in, "scratch") && !boolOf(in, "palette") {
			s.any, s.anyW, s.anyH = images[0], w, h
			mode := strings.TrimSpace(str(in, "mode"))
			if !boolOf(in, "survey") && str(in, "compare") == "" && str(in, "hold") == "" &&
				str(in, "crop") == "" && (mode == "" || mode == "gallery" || mode == "normal") {
				s.Latest, s.LatestW, s.LatestH, s.LatestT = images[0], w, h, s.lastT
			}
		}
	case "note":
		if isErr {
			e["error"] = text
		} else {
			e["reply"] = text
		}
	case "read":
		if isErr {
			e["error"] = text
		}
	default:
		if isErr {
			e["error"] = text
		} else {
			if len(text) > 2000 {
				text = text[:2000]
			}
			e["reply"] = text
		}
	}
	s.add(e)
}

// LatestLook: the newest whole-canvas look, else the newest look of the
// painting of any kind.
func (s *Session) LatestLook() (string, int, int) {
	if s.Latest != "" {
		return s.Latest, s.LatestW, s.LatestH
	}
	return s.any, s.anyW, s.anyH
}

// rel makes a look's path relative to the studio (as the easel prints it,
// absolute) with forward slashes.
func (s *Session) rel(p string) string {
	if filepath.IsAbs(p) {
		if r, err := filepath.Rel(s.studio, p); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.ToSlash(r)
		}
	}
	return filepath.ToSlash(p)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}
