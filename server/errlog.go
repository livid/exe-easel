package main

// The error log: what went wrong, in the app and in the daemon, kept where
// a coding agent can read it — <repo>/logs/error.log, one JSON object a
// line, the newest last, rotated to error.log.1 past 5 MB. The app posts
// what its user met (POST /v1/log: a call that failed, an alert it showed,
// a script error, a picture or movie that didn't load); the daemon adds its
// own answers of 500 and up, failed jobs and heals, and easels that didn't
// open. GET /v1/log answers the newest lines.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const errLogMax = 5 << 20

type ErrLog struct {
	Path string
	mu   sync.Mutex
	seen map[string]time.Time // the same error from the same source, once in 10 s
}

func NewErrLog(path string) *ErrLog { return &ErrLog{Path: path, seen: map[string]time.Time{}} }

// Add appends one entry: src is "app" or "daemon", level "error" or
// "warn", where says what was being done (a call, a tab, a job).
func (l *ErrLog) Add(src, level, studio, where, msg string, extra map[string]any) {
	if l == nil || l.Path == "" {
		return
	}
	msg = clip(msg, 4000)
	l.mu.Lock()
	defer l.mu.Unlock()
	key := src + "\x00" + studio + "\x00" + where + "\x00" + msg
	if t, ok := l.seen[key]; ok && time.Since(t) < 10*time.Second {
		return
	}
	l.seen[key] = time.Now()
	if len(l.seen) > 500 {
		for k, t := range l.seen {
			if time.Since(t) > time.Minute {
				delete(l.seen, k)
			}
		}
	}
	e := map[string]any{}
	for k, v := range extra {
		if s, ok := v.(string); ok {
			v = clip(s, 4000)
		}
		e[k] = v
	}
	e["t"] = time.Now().UTC().Format(time.RFC3339Nano)
	e["src"], e["level"], e["msg"] = src, level, msg
	if studio != "" {
		e["studio"] = studio
	}
	if where != "" {
		e["where"] = where
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(l.Path), 0o755)
	if fi, err := os.Stat(l.Path); err == nil && fi.Size() > errLogMax {
		os.Rename(l.Path, l.Path+".1")
	}
	f, err := os.OpenFile(l.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	f.Write(append(b, '\n'))
	f.Close()
}

// Tail returns the newest n entries, oldest first.
func (l *ErrLog) Tail(n int) []json.RawMessage {
	out := []json.RawMessage{}
	if l == nil {
		return out
	}
	f, err := os.Open(l.Path)
	if err != nil {
		return out
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			lines = append(lines, t)
			if len(lines) > n {
				lines = lines[1:]
			}
		}
	}
	for _, ln := range lines {
		if json.Valid([]byte(ln)) {
			out = append(out, json.RawMessage(ln))
		}
	}
	return out
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
