package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var he *httpError
	if errors.As(err, &he) {
		writeJSON(w, he.code, map[string]string{"error": he.msg})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 4<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return &httpError{400, "bad JSON: " + err.Error()}
	}
	return nil
}

// answerSlow sends the headers now and the answer when it is ready: exe's
// relay gives up on a service that hasn't sent its headers in 30 s, and a
// chunk can run for minutes. A space goes out every 15 s meanwhile (JSON
// passes over it) so no proxy takes the line for idle. With the headers
// out the status is 200 whatever happens: the body is the answer, or
// {"opening": words} while the easel opens, or {"error", "status"}.
func (d *Daemon) answerSlow(w http.ResponseWriter, r *http.Request, st *studioState, work func() (any, error)) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	fl, _ := w.(http.Flusher)
	if fl != nil {
		fl.Flush()
	}
	type result struct {
		v   any
		err error
	}
	ch := make(chan result, 1)
	go func() { v, err := work(); ch <- result{v, err} }()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	enc := json.NewEncoder(w)
	for {
		select {
		case res := <-ch:
			var oe *openingError
			var he *httpError
			switch {
			case res.err == nil:
				enc.Encode(res.v)
			case errors.As(res.err, &oe):
				enc.Encode(map[string]string{"opening": oe.progress})
			case errors.As(res.err, &he):
				if he.code >= 500 {
					d.Errors.Add("daemon", "error", st.name, r.Method+" "+r.URL.Path, he.msg, map[string]any{"status": he.code})
				}
				enc.Encode(map[string]any{"error": he.msg, "status": he.code})
			default:
				d.Errors.Add("daemon", "error", st.name, r.Method+" "+r.URL.Path, res.err.Error(), map[string]any{"status": 500})
				enc.Encode(map[string]any{"error": res.err.Error(), "status": 500})
			}
			return
		case <-tick.C:
			w.Write([]byte(" "))
			if fl != nil {
				fl.Flush()
			}
		}
	}
}

// statusWriter keeps what a handler answered, to log a 500 and up.
type statusWriter struct {
	http.ResponseWriter
	code int
	body []byte
}

func (s *statusWriter) WriteHeader(code int) { s.code = code; s.ResponseWriter.WriteHeader(code) }
func (s *statusWriter) Write(b []byte) (int, error) {
	if s.code >= 500 && len(s.body) < 4000 {
		s.body = append(s.body, b...)
	}
	return s.ResponseWriter.Write(b)
}
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Handler is the daemon's HTTP API (API.md).
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, code: 200}
		mux.ServeHTTP(sw, r)
		if sw.code >= 500 {
			msg := strings.TrimSpace(string(sw.body))
			var e struct{ Error string }
			if json.Unmarshal(sw.body, &e) == nil && e.Error != "" {
				msg = e.Error
			}
			d.Errors.Add("daemon", "error", r.PathValue("name"), r.Method+" "+r.URL.Path, msg, map[string]any{"status": sw.code})
		}
	})
	// the app's own errors, and the newest of everyone's
	mux.HandleFunc("POST /v1/log", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(nil, r.Body, 64<<10)
		var e map[string]any
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			writeErr(w, &httpError{400, "bad JSON: " + err.Error()})
			return
		}
		str := func(k string) string { v, _ := e[k].(string); delete(e, k); return v }
		level, studio, where, msg := str("level"), str("studio"), str("where"), str("msg")
		if level != "warn" {
			level = "error"
		}
		if msg == "" {
			writeErr(w, &httpError{400, "msg is what went wrong"})
			return
		}
		delete(e, "t")
		delete(e, "src")
		d.Errors.Add("app", level, studio, where, msg, e)
		w.WriteHeader(http.StatusNoContent)
	})
	// what the preflight found missing: the app says it when it opens
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		problems := append([]string{}, d.Problems...)
		d.mu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": len(problems) == 0, "problems": problems})
	})
	mux.HandleFunc("GET /v1/log", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n <= 0 || n > 1000 {
			n = 100
		}
		writeJSON(w, 200, map[string]any{"path": d.Errors.Path, "entries": d.Errors.Tail(n)})
	})
	mux.HandleFunc("GET /v1/studios", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"studios": d.List()})
	})
	mux.HandleFunc("GET /v1/events", d.events)
	mux.HandleFunc("POST /v1/studios", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Name, Profile, Brief string }
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		st, err := d.Create(req.Name, req.Profile, req.Brief)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 201, d.object(st))
		d.Kick()
	})
	mux.HandleFunc("GET /v1/profiles", func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]string{}
		for _, p := range profiles {
			out = append(out, map[string]string{"name": p.Name, "label": p.Label})
		}
		writeJSON(w, 200, map[string]any{"profiles": out})
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"models":   models,
			"efforts":  efforts,
			"defaults": map[string]any{"model": models[0].ID, "effort": "high", "hours": 4},
		})
	})
	mux.HandleFunc("GET /v1/templates/free", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"text": d.templateFree()})
	})
	mux.HandleFunc("GET /v1/studios/{name}", d.withStudio(d.detail))
	mux.HandleFunc("PUT /v1/studios/{name}/brief", d.withStudio(func(w http.ResponseWriter, r *http.Request, st *studioState) {
		var req struct{ Text string }
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		if err := d.PutBrief(st, req.Text); err != nil {
			writeErr(w, err)
			return
		}
		d.Kick()
		writeJSON(w, 200, d.object(st))
	}))
	mux.HandleFunc("DELETE /v1/studios/{name}", d.withStudio(func(w http.ResponseWriter, r *http.Request, st *studioState) {
		if err := d.Delete(st); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /v1/studios/{name}/session", d.withStudio(func(w http.ResponseWriter, r *http.Request, st *studioState) {
		after, _ := strconv.Atoi(r.URL.Query().Get("after"))
		st.mu.Lock()
		st.sess.Update()
		events, next, pending := st.sess.Events(after)
		st.mu.Unlock()
		writeJSON(w, 200, map[string]any{"events": events, "next": next, "pending": pending})
	}))
	mux.HandleFunc("GET /v1/studios/{name}/files/{path...}", d.withStudio(d.file))
	mux.HandleFunc("POST /v1/studios/{name}/start", d.withStudio(func(w http.ResponseWriter, r *http.Request, st *studioState) {
		var req StartReq
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		if err := d.Start(st, req); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, d.object(st))
	}))
	mux.HandleFunc("POST /v1/studios/{name}/stop", d.withStudio(func(w http.ResponseWriter, r *http.Request, st *studioState) {
		if err := d.Stop(st); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, d.object(st))
	}))
	mux.HandleFunc("POST /v1/studios/{name}/finish", d.withStudio(func(w http.ResponseWriter, r *http.Request, st *studioState) {
		var req FinishReq
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		if err := d.Finish(st, req); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, d.object(st))
	}))
	mux.HandleFunc("POST /v1/studios/{name}/clip", d.withStudio(func(w http.ResponseWriter, r *http.Request, st *studioState) {
		var req ClipReq
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		if err := d.Clip(st, req); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, d.object(st))
	}))
	mux.HandleFunc("POST /v1/studios/{name}/look", d.withStudio(func(w http.ResponseWriter, r *http.Request, st *studioState) {
		var req LookReq
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		d.answerSlow(w, r, st, func() (any, error) { return d.Look(st, req) })
	}))
	mux.HandleFunc("POST /v1/studios/{name}/do", d.withStudio(func(w http.ResponseWriter, r *http.Request, st *studioState) {
		var req DoReq
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		d.answerSlow(w, r, st, func() (any, error) {
			reply, err := d.Do(st, req)
			if err != nil {
				return nil, err
			}
			d.Kick()
			return map[string]string{"reply": reply}, nil
		})
	}))
	mux.HandleFunc("POST /v1/studios/{name}/close", d.withStudio(func(w http.ResponseWriter, r *http.Request, st *studioState) {
		if err := d.CloseEasel(st); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	return logged
}

func (d *Daemon) withStudio(h func(http.ResponseWriter, *http.Request, *studioState)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st := d.state(r.PathValue("name"))
		if st == nil {
			writeJSON(w, 404, map[string]string{"error": "no studio " + r.PathValue("name")})
			return
		}
		h(w, r, st)
	}
}

func (d *Daemon) detail(w http.ResponseWriter, r *http.Request, st *studioState) {
	o := d.object(st)
	brief, _ := os.ReadFile(filepath.Join(st.dir, "BRIEF.md"))
	reply, _ := os.ReadFile(filepath.Join(st.dir, "out/claude/reply.txt"))
	ri := readRuns(filepath.Join(st.dir, "out/claude/runs.log"))
	if ri.lines == nil {
		ri.lines = []string{}
	}
	st.mu.Lock()
	notify := st.notify
	st.mu.Unlock()
	b, _ := json.Marshal(o)
	var m map[string]any
	json.Unmarshal(b, &m)
	m["brief"] = string(brief)
	m["reply"] = string(reply)
	m["runs"] = ri.lines
	m["notify"] = notify
	writeJSON(w, 200, m)
}

// allowedRel: the files the app may read (API.md, files).
func allowedRel(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") || path.Clean(rel) != rel ||
		rel == ".." || strings.HasPrefix(rel, "../") {
		return false
	}
	switch {
	case rel == "BRIEF.md":
		return true
	case strings.HasPrefix(rel, "out/"), strings.HasPrefix(rel, "notes/"):
		return true
	case path.Dir(rel) == "paintings/lua" && path.Ext(rel) == ".lua":
		return true
	}
	return false
}

// fenced resolves a studio file the app may read: the path and what its
// links resolve to must both be allowed and inside the studio.
func fenced(studio, rel string) (string, bool) {
	if !allowedRel(rel) {
		return "", false
	}
	root, err := filepath.EvalSymlinks(studio)
	if err != nil {
		return "", false
	}
	real, err := filepath.EvalSymlinks(filepath.Join(studio, filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	r, err := filepath.Rel(root, real)
	if err != nil || !allowedRel(filepath.ToSlash(r)) {
		return "", false
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	return real, true
}

var textTypes = map[string]string{
	".md": "text/plain; charset=utf-8", ".lua": "text/plain; charset=utf-8", ".txt": "text/plain; charset=utf-8",
	".log": "text/plain; charset=utf-8", ".jsonl": "text/plain; charset=utf-8", ".tsv": "text/plain; charset=utf-8",
	".json": "application/json",
}

func (d *Daemon) file(w http.ResponseWriter, r *http.Request, st *studioState) {
	rel := r.PathValue("path")
	abs, ok := fenced(st.dir, rel)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "no such file in the studio: " + rel})
		return
	}
	ext := strings.ToLower(path.Ext(rel))
	immutable := ext == ".png" && (strings.HasPrefix(rel, "out/easel/") || strings.HasPrefix(rel, "out/app/looks/"))
	if wq := r.URL.Query().Get("w"); wq != "" && (ext == ".png" || ext == ".jpg" || ext == ".jpeg") {
		width, err := strconv.Atoi(wq)
		if err != nil || width < 16 || width > 2400 {
			writeJSON(w, 400, map[string]string{"error": "w: 16 to 2400"})
			return
		}
		thumb, err := thumbnail(st.dir, rel, abs, width)
		if err != nil {
			writeErr(w, err)
			return
		}
		abs, ext = thumb, ".jpg"
	}
	f, err := os.Open(abs)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer f.Close()
	fi, _ := f.Stat()
	if t, ok := textTypes[ext]; ok {
		w.Header().Set("Content-Type", t)
		w.Header().Set("Cache-Control", "no-store")
	} else if immutable || r.URL.Query().Get("v") != "" {
		// a look never changes; any other file asked for by version (the app
		// adds ?v=<the studio's updated stamp> to the finished picture) is
		// that version for good: a newer one has a new URL
		w.Header().Set("Cache-Control", "max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, path.Base(abs), fi.ModTime(), f)
}

func (d *Daemon) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, 500, map[string]string{"error": "no streaming"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	c := d.hub.Add()
	defer d.hub.Remove(c)
	w.Write([]byte("event: hello\ndata: {}\n\n"))
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-c:
			if _, err := w.Write(msg); err != nil {
				return
			}
			fl.Flush()
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			fl.Flush()
		}
	}
}
