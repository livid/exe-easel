package main

// Self-heal: what a studio's painting gives — the finished picture and the
// replay — the daemon makes by itself, without a button. Every ten seconds
// it looks for the studios that want something made and starts each as an
// automatic job: one a studio, never while its painter works or the app's
// own easel is open, and only once the log has rested for HealQuiet, so a
// hand still painting isn't chased after every chunk. Up to HealJobs run at
// once across the machine, and the order is what the window waits for
// most: every finished picture first (a few seconds each, so they don't
// count against HealJobs), then the views, then the replays, each kind
// newest log first.
// A painter starting, a hand at the easel, or a finish or replay asked for
// cancels an automatic job (yieldAuto); the next pass picks it up again.
//
//   - The finished picture is made when the painter ended by itself (the
//     last run in runs.log ended with status 0, not stopped) and there is none, and made
//     again when the log has moved on past it, with the options it was last
//     finished with (out/app/finish.json).
//   - The replay is made when there is none, or the log has moved on past
//     it, at the length last asked for (out/app/clip.json, else 75 s).
//   - The views (views.go) are drawn when missing or older than the log,
//     before the replay: they are what the window shows at once. A studio
//     that wants both has them drawn and filmed side by side (viewsAndClip):
//     two replays of the log, each on a core or two, that need nothing from
//     each other.
//
// A heal that fails is written to out/app/heal.json with the log's stamp,
// and not tried again until the log changes: a painting too short to film
// would otherwise be filmed every ten seconds.

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
)

const healEvery = 10 * time.Second

type healRecord struct {
	Log   string `json:"log"` // the log's stamp it failed on
	Error string `json:"error"`
	At    int64  `json:"at"`
}

func healPath(dir string) string { return filepath.Join(dir, "out/app/heal.json") }

func readHeal(dir string) map[string]healRecord {
	m := map[string]healRecord{}
	if b, err := os.ReadFile(healPath(dir)); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

// healFailed records an automatic job's failure against the log as it is.
func healFailed(dir, kind string, err error) {
	stamp, _, _ := stampOf(filepath.Join(dir, "paintings/lua/painting.lua"))
	m := readHeal(dir)
	m[kind] = healRecord{Log: stamp, Error: err.Error(), At: time.Now().UnixMilli()}
	if b, e := json.MarshalIndent(m, "", "  "); e == nil {
		os.MkdirAll(filepath.Dir(healPath(dir)), 0o755)
		os.WriteFile(healPath(dir), b, 0o644)
	}
}

// lastRunClean: the last run in runs.log ended by itself with status 0. A
// run the daemon's Stop ended has a "stop" line before its end (Stop writes
// it): claude interrupted exits 0 too, and a painting stopped to be resumed
// later isn't finished. The time limit (paint's own watchdog) writes none:
// at the limit the log is the painting.
func lastRunClean(dir string) bool {
	f, err := os.Open(filepath.Join(dir, "out/claude/runs.log"))
	if err != nil {
		return false
	}
	defer f.Close()
	clean, stopped := false, false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		ln := strings.TrimSpace(sc.Text())
		w := strings.Fields(ln)
		if len(w) < 2 {
			continue
		}
		switch w[1] {
		case "start":
			clean, stopped = false, false
		case "stop":
			stopped = true
		case "end":
			clean = !stopped && strings.Contains(ln, " end status=0")
		}
	}
	return clean
}

// healNeed says what the studio in dir wants made first: "finish",
// "views", "clip" or "".
func healNeed(dir string, quiet time.Duration, now time.Time) string {
	if needs := healNeeds(dir, quiet, now); len(needs) > 0 {
		return needs[0]
	}
	return ""
}

// healNeeds lists all the studio in dir wants made, in the order they are
// made: "finish", "views", "clip".
func healNeeds(dir string, quiet time.Duration, now time.Time) []string {
	logPath := filepath.Join(dir, "paintings/lua/painting.lua")
	stamp, logTime, ok := stampOf(logPath)
	if !ok || now.Sub(logTime) < quiet || countChunks(logPath) == 0 {
		return nil
	}
	failed := readHeal(dir)
	olderThanLog := func(rel string) (bool, bool) {
		fi, err := os.Stat(filepath.Join(dir, rel))
		if err != nil {
			return false, false
		}
		return fi.ModTime().Before(logTime), true
	}
	var needs []string
	if failed["finish"].Log != stamp {
		if stale, there := olderThanLog("out/final.png"); stale || (!there && lastRunClean(dir)) {
			needs = append(needs, "finish")
		}
	}
	if failed["views"].Log != stamp && !viewsFresh(dir) {
		needs = append(needs, "views")
	}
	if failed["clip"].Log != stamp {
		if stale, there := olderThanLog("out/replay.mp4"); stale || !there {
			needs = append(needs, "clip")
		}
	}
	return needs
}

func savedFinish(dir string) FinishReq {
	var r FinishReq
	if b, err := os.ReadFile(filepath.Join(dir, "out/app/finish.json")); err == nil {
		json.Unmarshal(b, &r)
	}
	r.Replay = 0
	return r
}

func savedClipLength(dir string) float64 {
	var r ClipReq
	if b, err := os.ReadFile(filepath.Join(dir, "out/app/clip.json")); err == nil {
		json.Unmarshal(b, &r)
	}
	if r.Length < 5 || r.Length > 600 {
		return 75
	}
	return r.Length
}

// defaultHealJobs: a sixth of the cores, at least one. A replay keeps a
// core or two busy (its own easel at RAYON_NUM_THREADS=4) and a studio's
// views and clip run two, so a sixth leaves the painters most of the
// machine: 3 on a 20-core Linux box, 1 on an 8-core Mac.
func defaultHealJobs() int {
	return max(1, runtime.NumCPU()/6)
}

// healWant is one studio's heal, waiting its turn.
type healWant struct {
	st      *studioState
	needs   []string // healNeeds; the first is what starts
	logTime time.Time
}

var healRank = map[string]int{"finish": 0, "views": 1, "clip": 2}

// healOrder sorts the wants the way they start: finishes first, then
// views, then clips; within a kind, the newest log first.
func healOrder(wants []healWant) {
	sort.SliceStable(wants, func(i, j int) bool {
		a, b := healRank[wants[i].needs[0]], healRank[wants[j].needs[0]]
		if a != b {
			return a < b
		}
		return wants[i].logTime.After(wants[j].logTime)
	})
}

// heal starts the automatic jobs the studios want, in healOrder: every
// finish, and replays while fewer than HealJobs run; called from the poll
// loop.
func (d *Daemon) heal() {
	if d.NoHeal || time.Since(d.healAt) < healEvery {
		return
	}
	d.healAt = time.Now()
	d.mu.Lock()
	sts := make([]*studioState, 0, len(d.studios))
	for _, st := range d.studios {
		sts = append(sts, st)
	}
	d.mu.Unlock()
	replays := 0
	for _, st := range sts {
		st.mu.Lock()
		if st.job != nil && st.job.Auto && st.job.Kind != "finish" {
			replays++
		}
		st.mu.Unlock()
	}
	now := time.Now()
	var wants []healWant
	for _, st := range sts {
		if d.painting(st) {
			continue
		}
		st.mu.Lock()
		skip := st.preparing || st.failed || st.job != nil || st.ownEasel || st.stopping ||
			(!st.launched.IsZero() && time.Since(st.launched) < time.Minute)
		st.mu.Unlock()
		if skip {
			continue
		}
		if needs := healNeeds(st.dir, d.HealQuiet, now); len(needs) > 0 {
			_, t, _ := stampOf(filepath.Join(st.dir, "paintings/lua/painting.lua"))
			wants = append(wants, healWant{st, needs, t})
		}
	}
	healOrder(wants)
	for _, w := range wants {
		replay := w.needs[0] != "finish"
		if replay && replays >= max(1, d.HealJobs) {
			break
		}
		if err := d.startHeal(w.st, w.needs); err != nil {
			continue
		}
		log.Printf("heal %s %s: started", w.needs[0], w.st.name)
		if replay {
			replays++
		}
	}
}

// startHeal starts the automatic job for the first of needs; views that
// come with a clip wanted too are drawn beside it (viewsAndClip).
func (d *Daemon) startHeal(st *studioState, needs []string) error {
	switch needs[0] {
	case "finish":
		r := savedFinish(st.dir)
		args, aerr := finishArgs(st, r)
		if aerr != nil {
			r = FinishReq{}
			args, _ = finishArgs(st, r)
		}
		return d.finish(st, r, args, true)
	case "views":
		if slices.Contains(needs, "clip") {
			length := savedClipLength(st.dir)
			return d.runJob(st, "views", true, func(ctx context.Context) error { return d.viewsAndClip(ctx, st, length) })
		}
		return d.runJob(st, "views", true, func(ctx context.Context) error { return d.renderViews(ctx, st) })
	case "clip":
		length := savedClipLength(st.dir)
		return d.runJob(st, "clip", true, func(ctx context.Context) error { return d.clip(ctx, st, length) })
	}
	return nil
}

// viewsAndClip draws the views and films the replay side by side, so a
// painting just ended has both in the time of the longer. The job reads
// "views" until they are kept (a look for one waits on it meanwhile), then
// "clip". Views that fail are their own failure (the studio's error, the
// error log, heal.json) and the filming goes on; the job's error is the
// clip's.
func (d *Daemon) viewsAndClip(ctx context.Context, st *studioState, length float64) error {
	filmed := make(chan error, 1)
	go func() { filmed <- d.clip(ctx, st, length) }()
	if err := d.renderViews(ctx, st); err != nil && ctx.Err() == nil {
		st.mu.Lock()
		st.errMsg = err.Error()
		st.mu.Unlock()
		d.jobFailed(st, "views", true, err)
	}
	st.mu.Lock()
	st.job.Kind = "clip"
	st.mu.Unlock()
	d.Kick()
	return <-filmed
}
