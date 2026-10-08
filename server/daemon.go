package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Daemon holds the studios and everything the handlers share.
type Daemon struct {
	Repo     string // the exe-easel checkout: harness/claude, templates, logs
	Engine   string // claude-paint (the submodule): the easel, its scripts, the painter's notes
	Studios  string // the studios folder
	Claude   string // the claude binary
	Node     string // node 24
	Exe      string // exe's API, for POST /v1/push
	ExeToken string

	// injectable for tests
	Procs  func() []Proc
	Launch func(name, studio string, env []string) error
	Push   func(title, body, tag string) error

	mu      sync.Mutex
	studios map[string]*studioState
	ix      procIndex
	last    map[string][]byte // the studio object as last broadcast
	pollMu  sync.Mutex
	healAt  time.Time // the last heal pass
	// HealQuiet: how long a log stays unchanged before its picture and
	// replay are healed (tests set it to 0)
	HealQuiet time.Duration
	// NoHeal: no automatic jobs (tests: a heal started by the poll an
	// action kicks would outlive the test that set up its studio)
	NoHeal bool

	hub *Hub

	Errors *ErrLog // <repo>/logs/error.log
}

func NewDaemon(repo, studios string) *Daemon {
	d := &Daemon{Repo: repo, Engine: filepath.Join(repo, "engine"), Studios: studios, studios: map[string]*studioState{}, last: map[string][]byte{}, hub: NewHub(), HealQuiet: 2 * time.Minute,
		Errors: NewErrLog(filepath.Join(repo, "logs/error.log"))}
	d.Procs = scanProcs
	d.Launch = d.launchPainter
	d.Push = d.pushExe
	return d
}

// state returns the studio's state, creating it for a folder that is a studio.
func (d *Daemon) state(name string) *studioState {
	if !nameRe.MatchString(name) {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if st := d.studios[name]; st != nil {
		return st
	}
	dir := filepath.Join(d.Studios, name)
	if !isStudio(dir) {
		return nil
	}
	st := newStudioState(name, dir)
	st.notify = exists(filepath.Join(dir, "out/app/notify"))
	d.studios[name] = st
	return st
}

func isStudio(dir string) bool {
	return exists(filepath.Join(dir, "bin/easel")) && exists(filepath.Join(dir, "BRIEF.md"))
}

// scan refreshes the process index and the set of studios.
func (d *Daemon) scan() {
	ix := indexProcs(d.Procs())
	ents, _ := os.ReadDir(d.Studios)
	d.mu.Lock()
	d.ix = ix
	seen := map[string]bool{}
	for _, e := range ents {
		name := e.Name()
		if !e.IsDir() || !nameRe.MatchString(name) {
			continue
		}
		dir := filepath.Join(d.Studios, name)
		if d.studios[name] == nil {
			if !isStudio(dir) {
				continue
			}
			st := newStudioState(name, dir)
			st.notify = exists(filepath.Join(dir, "out/app/notify"))
			d.studios[name] = st
		}
		seen[name] = true
	}
	for name, st := range d.studios {
		if seen[name] {
			continue
		}
		st.mu.Lock()
		keep := st.preparing || st.failed
		st.mu.Unlock()
		if !keep {
			delete(d.studios, name)
		}
	}
	d.mu.Unlock()
}

func (d *Daemon) painting(st *studioState) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.ix.painters[st.dir]
	return ok
}

// paintingNow scans the processes again: an action never trusts the last
// poll's scan.
func (d *Daemon) paintingNow(st *studioState) bool {
	ix := indexProcs(d.Procs())
	d.mu.Lock()
	d.ix = ix
	_, ok := ix.painters[st.dir]
	d.mu.Unlock()
	return ok
}

// object computes one studio's object, and notifies when a run it was
// asked to notify for has ended.
func (d *Daemon) object(st *studioState) Studio {
	painting := d.painting(st)
	st.mu.Lock()
	o := st.compute(painting)
	busy := o.State == "painting" || o.State == "stopping"
	push := st.wasPaint && !busy && st.notify
	st.wasPaint = busy
	if push {
		st.notify = false
		os.Remove(filepath.Join(st.dir, "out/app/notify"))
	}
	st.mu.Unlock()
	if push {
		body := st.name + ": "
		if o.Title != "" {
			body += o.Title
		} else {
			body += "the painter stopped"
		}
		go func() {
			if err := d.Push("Easel", body, "easel-"+st.name); err != nil {
				log.Printf("push %s: %v", st.name, err)
			}
		}()
	}
	return o
}

// List scans and returns every studio, newest created first.
func (d *Daemon) List() []Studio {
	d.scan()
	d.mu.Lock()
	sts := make([]*studioState, 0, len(d.studios))
	for _, st := range d.studios {
		sts = append(sts, st)
	}
	d.mu.Unlock()
	out := make([]Studio, 0, len(sts))
	for _, st := range sts {
		out = append(out, d.object(st))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created != out[j].Created {
			return out[i].Created > out[j].Created
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Adopt takes the easels left open with no painter at them (a restart
// forgot which it had opened) as the daemon's, so they close when idle.
func (d *Daemon) Adopt() {
	d.scan()
	d.mu.Lock()
	sts := make([]*studioState, 0, len(d.studios))
	for _, st := range d.studios {
		sts = append(sts, st)
	}
	d.mu.Unlock()
	for _, st := range sts {
		if d.painting(st) || !exists(filepath.Join(st.dir, "out/easel/painting/sock")) {
			continue
		}
		st.mu.Lock()
		st.ownEasel, st.lastUse = true, time.Now()
		st.mu.Unlock()
	}
}

// Poll broadcasts every studio whose object changed, and removals, once a second.
func (d *Daemon) Poll(stop <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		d.pollOnce()
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

func (d *Daemon) pollOnce() {
	d.pollMu.Lock()
	defer d.pollMu.Unlock()
	list := d.List()
	now := map[string]bool{}
	for _, o := range list {
		now[o.Name] = true
		b, _ := json.Marshal(o)
		if !bytes.Equal(d.last[o.Name], b) {
			d.last[o.Name] = b
			d.hub.Send("studio", b)
		}
	}
	for name := range d.last {
		if !now[name] {
			delete(d.last, name)
			b, _ := json.Marshal(map[string]string{"name": name})
			d.hub.Send("removed", b)
		}
	}
	d.closeIdle()
	d.heal()
}

// Kick re-polls at once (after an action) so the stream answers quickly.
func (d *Daemon) Kick() { go d.pollOnce() }

func (d *Daemon) pushExe(title, body, tag string) error {
	if d.Exe == "" {
		return nil
	}
	b, _ := json.Marshal(map[string]string{"title": title, "body": body, "tag": tag, "url": "/"})
	req, err := http.NewRequest("POST", strings.TrimRight(d.Exe, "/")+"/v1/push", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if d.ExeToken != "" {
		req.Header.Set("Authorization", "Bearer "+d.ExeToken)
	}
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return &httpError{resp.StatusCode, "push: " + resp.Status}
	}
	return nil
}

// Hub fans server-sent events out to the connected streams.
type Hub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func NewHub() *Hub { return &Hub{clients: map[chan []byte]struct{}{}} }

func (h *Hub) Add() chan []byte {
	c := make(chan []byte, 256)
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *Hub) Remove(c chan []byte) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *Hub) Send(event string, data []byte) {
	msg := []byte("event: " + event + "\ndata: " + string(data) + "\n\n")
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c <- msg:
		default: // a stream that fell 256 events behind loses this one
		}
	}
}

type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }
