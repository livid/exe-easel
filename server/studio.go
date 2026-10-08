package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

// Job is a finish, clip or export running for a studio.
type Job struct {
	Kind    string `json:"kind"`
	Started int64  `json:"started"`
	Error   string `json:"error"`
	// Auto: the daemon started it itself, healing a missing or stale
	// picture or replay (heal.go); a painter or a hand at the easel
	// cancels it rather than wait for it
	Auto bool `json:"auto"`
}

// Studio is the object API.md describes.
type Studio struct {
	Name    string  `json:"name"`
	Title   string  `json:"title"`
	State   string  `json:"state"`
	Job     *Job    `json:"job"`
	Error   string  `json:"error"`
	Box     string  `json:"box"`
	Model   string  `json:"model"`
	Effort  string  `json:"effort"`
	Chunks  int     `json:"chunks"`
	Looks   int     `json:"looks"`
	Clock   string  `json:"clock"`
	Canvas  *Size   `json:"canvas"`
	Latest  string  `json:"latest"`
	Final   bool    `json:"final"`
	Clip    bool    `json:"clip"`
	Created int64   `json:"created"`
	Updated int64   `json:"updated"`
	Started *int64  `json:"started"`
	Ended   *int64  `json:"ended"`
	Exit    *int    `json:"exit"`
	Tokens  Tokens  `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
}

type Size struct {
	W int `json:"w"`
	H int `json:"h"`
}

// studioState is what the daemon holds for one studio between polls.
type studioState struct {
	mu      sync.Mutex
	easelMu sync.Mutex // the app's easel commands, one at a time
	name    string
	dir     string

	sess *Session

	preparing bool
	failed    bool
	errMsg    string
	job       *Job
	cancel    func() // the running job's, to cancel an automatic one
	jobDone   chan struct{}
	stopping  bool
	launched  time.Time // a start asked; the scan may not see it for a moment
	notify    bool
	wasPaint  bool

	// an easel the daemon opened for look/do
	ownEasel bool
	opening  bool   // its `easel open` (a replay of the log) is running
	openErr  string // how the last open failed, said once
	lastUse  time.Time

	// the log's chunk count, by its stamp
	logStamp   string
	chunks     int
	replyStamp string
	title      string

	// the app's newest whole-canvas look (out/app/looks.jsonl)
	appStamp string
	appLook  appLook
	hasApp   bool
}

func newStudioState(name, dir string) *studioState {
	return &studioState{name: name, dir: dir, sess: NewSession(filepath.Join(dir, "out/claude/session.jsonl"), dir)}
}

func stampOf(path string) (string, time.Time, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", time.Time{}, false
	}
	return fi.ModTime().String() + "|" + itoa64(fi.Size()), fi.ModTime(), true
}

func itoa64(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

var tsRe = regexp.MustCompile(`^(\S+) (start|end)\b(.*)$`)

// runsInfo reads runs.log: the last start (model, effort) and its end.
type runsInfo struct {
	lines   []string
	model   string
	effort  string
	started *int64
	ended   *int64
	exit    *int
}

func readRuns(path string) runsInfo {
	var ri runsInfo
	b, err := os.ReadFile(path)
	if err != nil {
		return ri
	}
	for _, ln := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if ln == "" {
			continue
		}
		ri.lines = append(ri.lines, ln)
		m := tsRe.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		t, err := time.Parse(time.RFC3339, m[1])
		if err != nil {
			continue
		}
		ms := t.UnixMilli()
		fields := map[string]string{}
		for _, f := range strings.Fields(m[3]) {
			if k, v, ok := strings.Cut(f, "="); ok {
				fields[k] = v
			}
		}
		if m[2] == "start" {
			ri.started, ri.ended, ri.exit = &ms, nil, nil
			ri.model, ri.effort = fields["model"], fields["effort"]
		} else {
			ri.ended = &ms
			if v, ok := fields["status"]; ok {
				n := atoi(v)
				ri.exit = &n
			}
		}
	}
	return ri
}

// countChunks counts "--@ chunk" lines.
func countChunks(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		if bytes.HasPrefix(sc.Bytes(), []byte("--@ chunk")) {
			n++
		}
	}
	return n
}

var (
	titlePrefixRe = regexp.MustCompile(`(?i)^title\s*(:|—|–|-)\s*`)
	emphRe        = regexp.MustCompile(`\*\*|\*|__|(^|\s)_|_(\s|$)`)
)

// workNameRe: a work's name in the reply's first paragraph, between the
// brackets Chinese and Japanese give titles: 《冰箱的光》, 「石下的眼睛」.
var workNameRe = regexp.MustCompile(`《([^《》\n]{1,60})》|「([^「」\n]{1,60})」`)

// titleFrom: the name the painter gave its picture (API.md, Title). A name
// in 《》 or 「」 in the first paragraph wins: a painter that answers "It's
// called 《手指画的星》" in a sentence, or puts an English gloss after it,
// still gets the name alone. Otherwise the first non-empty line, cleaned.
func titleFrom(reply string) string {
	first := strings.TrimSpace(reply)
	if i := strings.Index(first, "\n\n"); i >= 0 {
		first = first[:i]
	}
	if m := workNameRe.FindStringSubmatch(first); m != nil {
		if name := strings.TrimSpace(m[1] + m[2]); name != "" {
			return name
		}
	}
	for _, ln := range strings.Split(reply, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		t = strings.TrimSpace(strings.TrimLeft(t, "#"))
		t = emphRe.ReplaceAllStringFunc(t, func(m string) string {
			return strings.Trim(m, "*_")
		})
		t = strings.TrimSpace(t)
		t = strings.TrimSpace(titlePrefixRe.ReplaceAllString(t, ""))
		t = trimQuotes(t)
		if t == "" || len([]rune(t)) > 120 {
			return ""
		}
		return t
	}
	return ""
}

func trimQuotes(t string) string {
	pairs := [][2]string{{"“", "”"}, {"\"", "\""}, {"‘", "’"}, {"《", "》"}, {"'", "'"}, {"「", "」"}}
	for changed := true; changed; {
		changed = false
		for _, p := range pairs {
			if len(t) >= len(p[0])+len(p[1]) && strings.HasPrefix(t, p[0]) && strings.HasSuffix(t, p[1]) {
				t = strings.TrimSpace(t[len(p[0]) : len(t)-len(p[1])])
				changed = true
			}
		}
	}
	return t
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// compute builds the studio object. painting comes from the process scan.
// The caller holds st.mu.
func (st *studioState) compute(painting bool) Studio {
	dir := st.dir
	o := Studio{Name: st.name, Box: "default", Job: st.job, Error: st.errMsg}
	if st.preparing {
		o.State = "preparing"
		return o
	}
	if st.failed {
		o.State = "failed"
		return o
	}
	if b, err := os.ReadFile(filepath.Join(dir, "bin/box")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			o.Box = s
		}
	}
	var updated time.Time
	bump := func(p string) (string, bool) {
		stamp, mt, ok := stampOf(p)
		if ok && mt.After(updated) {
			updated = mt
		}
		return stamp, ok
	}
	if _, mt, ok := stampOf(filepath.Join(dir, "BRIEF.md")); ok {
		o.Created = mt.UnixMilli()
	}
	st.sess.Update()
	bump(st.sess.path)
	logPath := filepath.Join(dir, "paintings/lua/painting.lua")
	if stamp, ok := bump(logPath); ok {
		if stamp != st.logStamp {
			st.logStamp, st.chunks = stamp, countChunks(logPath)
		}
	} else {
		st.logStamp, st.chunks = "", 0
	}
	bump(filepath.Join(dir, "notes/journal.md"))
	bump(filepath.Join(dir, "out/claude/runs.log"))
	_, o.Final = bump(filepath.Join(dir, "out/final.png"))
	_, o.Clip = bump(filepath.Join(dir, "out/replay.mp4"))
	replyPath := filepath.Join(dir, "out/claude/reply.txt")
	if stamp, _, ok := stampOf(replyPath); ok {
		if stamp != st.replyStamp {
			b, _ := os.ReadFile(replyPath)
			st.replyStamp, st.title = stamp, titleFrom(string(b))
		}
	} else {
		st.replyStamp, st.title = "", ""
	}
	o.Title = st.title
	if !updated.IsZero() {
		o.Updated = updated.UnixMilli()
	}
	ri := readRuns(filepath.Join(dir, "out/claude/runs.log"))
	o.Model, o.Effort, o.Started, o.Ended, o.Exit = ri.model, ri.effort, ri.started, ri.ended, ri.exit
	o.Chunks = st.chunks
	o.Looks = st.sess.Looks
	o.Clock = st.sess.Clock
	p, w, h := st.sess.LatestLook()
	appStamp, _, _ := stampOf(filepath.Join(dir, "out/app/looks.jsonl"))
	if appStamp != st.appStamp {
		st.appStamp = appStamp
		st.appLook, st.hasApp = lastAppLook(dir)
	}
	if st.hasApp && (st.sess.Latest == "" || st.appLook.T > st.sess.LatestT) {
		p, w, h = st.appLook.Path, st.appLook.W, st.appLook.H
	}
	if p != "" {
		o.Latest = p
		if w > 0 && h > 0 {
			o.Canvas = &Size{W: w, H: h}
		}
	}
	o.Tokens = st.sess.Tokens
	o.CostUSD = st.sess.Cost

	// a start just asked: the scan may not see the painter for a moment
	if painting {
		st.launched = time.Time{}
	} else if !st.launched.IsZero() && time.Since(st.launched) < 15*time.Second {
		painting = true
	}
	switch {
	case painting && st.stopping:
		o.State = "stopping"
	case painting:
		o.State = "painting"
	case st.job != nil && st.job.Kind == "finish":
		o.State = "finishing"
	case st.job != nil && st.job.Kind == "clip":
		o.State = "replaying"
	default:
		o.State = "idle"
	}
	if !painting {
		st.stopping = false
	}
	return o
}
