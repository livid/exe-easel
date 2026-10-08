package main

// Self-heal: what a studio's painting gives — the finished picture and the
// replay — the daemon makes by itself, without a button. Every ten seconds
// it looks for one studio that wants something made and starts that as an
// automatic job: one at a time across the machine, never while a painter
// works or the app's own easel is open, and only once the log has rested
// for HealQuiet, so a hand still painting isn't chased after every chunk.
// A painter starting, a hand at the easel, or a finish or replay asked for
// cancels an automatic job (yieldAuto); the next pass picks it up again.
//
//   - The finished picture is made when the painter ended by itself (the
//     last run in runs.log ended with status 0) and there is none, and made
//     again when the log has moved on past it, with the options it was last
//     finished with (out/app/finish.json).
//   - The replay is made when there is none, or the log has moved on past
//     it, at the length last asked for (out/app/clip.json, else 75 s).
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

// lastRunClean: the last line of runs.log is a run that ended with status 0.
func lastRunClean(dir string) bool {
	f, err := os.Open(filepath.Join(dir, "out/claude/runs.log"))
	if err != nil {
		return false
	}
	defer f.Close()
	last := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			last = t
		}
	}
	return strings.Contains(last, " end status=0")
}

// healNeed says what the studio in dir wants made: "finish", "clip" or "".
func healNeed(dir string, quiet time.Duration, now time.Time) string {
	logPath := filepath.Join(dir, "paintings/lua/painting.lua")
	stamp, logTime, ok := stampOf(logPath)
	if !ok || now.Sub(logTime) < quiet || countChunks(logPath) == 0 {
		return ""
	}
	failed := readHeal(dir)
	olderThanLog := func(rel string) (bool, bool) {
		fi, err := os.Stat(filepath.Join(dir, rel))
		if err != nil {
			return false, false
		}
		return fi.ModTime().Before(logTime), true
	}
	if failed["finish"].Log != stamp {
		if stale, there := olderThanLog("out/final.png"); stale || (!there && lastRunClean(dir)) {
			return "finish"
		}
	}
	if failed["clip"].Log != stamp {
		if stale, there := olderThanLog("out/replay.mp4"); stale || !there {
			return "clip"
		}
	}
	return ""
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

// heal starts the one automatic job the studios want most, newest log
// first; called from the poll loop.
func (d *Daemon) heal() {
	if time.Since(d.healAt) < healEvery {
		return
	}
	d.healAt = time.Now()
	d.mu.Lock()
	sts := make([]*studioState, 0, len(d.studios))
	for _, st := range d.studios {
		sts = append(sts, st)
	}
	d.mu.Unlock()
	for _, st := range sts {
		st.mu.Lock()
		running := st.job != nil && st.job.Auto
		st.mu.Unlock()
		if running {
			return
		}
	}
	logTime := func(st *studioState) time.Time {
		_, t, _ := stampOf(filepath.Join(st.dir, "paintings/lua/painting.lua"))
		return t
	}
	sort.Slice(sts, func(i, j int) bool { return logTime(sts[i]).After(logTime(sts[j])) })
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
		var err error
		switch need := healNeed(st.dir, d.HealQuiet, time.Now()); need {
		case "finish":
			r := savedFinish(st.dir)
			args, aerr := finishArgs(st, r)
			if aerr != nil {
				r = FinishReq{}
				args, _ = finishArgs(st, r)
			}
			err = d.finish(st, r, args, true)
		case "clip":
			length := savedClipLength(st.dir)
			err = d.runJob(st, "clip", true, func(ctx context.Context) error { return d.clip(ctx, st, length) })
		default:
			continue
		}
		if err == nil {
			log.Printf("heal %s: started", st.name)
			return
		}
	}
}
