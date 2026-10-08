package main

// What the app asks of a studio as a whole: make one, start and stop its
// painter, finish the painting, cut its replay, delete it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var profiles = []struct{ Name, Label string }{
	{"every", "Every tube"},
	{"blank", "Blank (the tube box)"},
	{"friedrich", "Friedrich"},
	{"sargent", "Sargent"},
	{"inness", "Inness"},
	{"alma-tadema", "Alma-Tadema"},
	{"tonn", "Tonn"},
	{"hopper", "Hopper"},
	{"giverny", "Giverny"},
	{"impressionist", "Impressionist"},
}

var models = []struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}{
	{"claude-opus-5-5", "Claude Opus 5.5"},
	{"claude-fable-5-1", "Claude Fable 5.1"},
	{"claude-sonnet-5-5", "Claude Sonnet 5.5"},
	{"claude-haiku-5-5", "Claude Haiku 5.5"},
}

var efforts = []string{"low", "medium", "high", "xhigh", "max"}

func validProfile(p string) bool {
	for _, x := range profiles {
		if x.Name == p {
			return true
		}
	}
	return false
}

// env for the scripts and the painter: PATH with node, claude and cargo.
func (d *Daemon) env(extra ...string) []string {
	home := os.Getenv("HOME")
	dirs := []string{}
	add := func(p string) {
		if p == "" {
			return
		}
		for _, x := range dirs {
			if x == p {
				return
			}
		}
		dirs = append(dirs, p)
	}
	if d.Node != "" {
		add(filepath.Dir(d.Node))
	}
	if d.Claude != "" {
		add(filepath.Dir(d.Claude))
	}
	add(filepath.Join(home, ".cargo/bin"))
	add(filepath.Join(home, ".local/bin"))
	for _, p := range []string{"/usr/local/go/bin", "/usr/local/bin", "/usr/bin", "/bin"} {
		add(p)
	}
	env := []string{"PATH=" + strings.Join(dirs, ":"), "HOME=" + home}
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
		env = append(env, "XDG_RUNTIME_DIR="+v)
	} else {
		env = append(env, fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", os.Getuid()))
	}
	if v := os.Getenv("LANG"); v != "" {
		env = append(env, "LANG="+v)
	}
	return append(env, extra...)
}

func (d *Daemon) templateFree() string {
	b, err := os.ReadFile(filepath.Join(d.Repo, "templates/free.md"))
	if err != nil {
		return ""
	}
	return string(b)
}

// Create starts exporting a new studio; it is `preparing` until it is done.
func (d *Daemon) Create(name, profile, brief string) (*studioState, error) {
	if !nameRe.MatchString(name) {
		return nil, &httpError{400, "a studio's name is lower-case letters, digits and hyphens (at most 48), starting with a letter or digit"}
	}
	if profile == "" {
		profile = "every"
	}
	if !validProfile(profile) {
		return nil, &httpError{400, "no profile " + profile}
	}
	if strings.TrimSpace(brief) == "" {
		brief = d.templateFree()
	}
	dir := filepath.Join(d.Studios, name)
	d.mu.Lock()
	if d.studios[name] != nil || exists(dir) {
		d.mu.Unlock()
		return nil, &httpError{409, "there is a studio named " + name + " already"}
	}
	st := newStudioState(name, dir)
	st.preparing = true
	st.job = &Job{Kind: "export", Started: time.Now().UnixMilli()}
	d.studios[name] = st
	d.mu.Unlock()
	if err := os.MkdirAll(d.Studios, 0o755); err != nil {
		return nil, err
	}
	go func() {
		cmd := exec.Command(filepath.Join(d.Repo, "scripts/export_r16_studio"), profile, dir)
		cmd.Dir = d.Repo
		cmd.Env = d.env("R16_BRANCH=main", "TMPDIR="+os.TempDir())
		out, err := cmd.CombinedOutput()
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, "BRIEF.md"), []byte(brief), 0o644)
		}
		st.mu.Lock()
		st.preparing = false
		st.job = nil
		if err != nil {
			st.failed = true
			st.errMsg = lastLines(string(out)+"\n"+err.Error(), 12)
			log.Printf("export %s: %v\n%s", name, err, out)
		}
		st.mu.Unlock()
		d.Kick()
	}()
	return st, nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// StartReq is POST …/start.
type StartReq struct {
	Model  string  `json:"model"`
	Effort string  `json:"effort"`
	Hours  float64 `json:"hours"`
	Notify bool    `json:"notify"`
}

func (d *Daemon) Start(st *studioState, r StartReq) error {
	if r.Model == "" {
		r.Model = models[0].ID
	}
	if r.Effort == "" {
		r.Effort = "high"
	}
	if r.Hours == 0 {
		r.Hours = 4
	}
	ok := false
	for _, m := range models {
		ok = ok || m.ID == r.Model
	}
	if !ok {
		return &httpError{400, "no model " + r.Model}
	}
	ok = false
	for _, e := range efforts {
		ok = ok || e == r.Effort
	}
	if !ok {
		return &httpError{400, "no effort " + r.Effort}
	}
	if r.Hours < 0.05 || r.Hours > 48 {
		return &httpError{400, "hours: 0.05 to 48"}
	}
	st.yieldAuto()
	st.easelMu.Lock()
	defer st.easelMu.Unlock()
	if err := d.easelBusy(st); err != nil {
		if err == errBusy {
			return &httpError{409, "a painter is at the easel already"}
		}
		return err
	}
	env := d.env(
		"PAINTER_MODEL="+r.Model,
		"PAINTER_EFFORT="+r.Effort,
		fmt.Sprintf("PAINTER_HOURS=%g", r.Hours),
		"RAYON_NUM_THREADS=3",
		"CLAUDE="+d.Claude,
		"NODE="+d.Node,
	)
	if err := d.Launch(st.name, st.dir, env); err != nil {
		return err
	}
	st.mu.Lock()
	st.launched = time.Now()
	st.stopping = false
	st.ownEasel = false // the painter's now
	st.notify = r.Notify
	st.mu.Unlock()
	notify := filepath.Join(st.dir, "out/app/notify")
	if r.Notify {
		os.MkdirAll(filepath.Dir(notify), 0o755)
		os.WriteFile(notify, []byte("1\n"), 0o644)
	} else {
		os.Remove(notify)
	}
	d.Kick()
	return nil
}

// launchPainter runs harness/claude/paint in a transient systemd user unit
// of its own, so it outlives the daemon; setsid when systemd-run can't.
func (d *Daemon) launchPainter(name, studio string, env []string) error {
	paint := filepath.Join(d.Repo, "harness/claude/paint")
	os.MkdirAll(filepath.Join(studio, "out/claude"), 0o755)
	args := []string{"--user", "--collect", "--quiet", "--unit", "exe-art-" + name, "--working-directory", studio}
	for _, e := range env {
		args = append(args, "--setenv="+e)
	}
	args = append(args, paint, studio)
	cmd := exec.Command("systemd-run", args...)
	cmd.Env = d.env()
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	// a unit of that name may linger: try a fresh name once
	args[4] = fmt.Sprintf("exe-art-%s-%d", name, time.Now().Unix())
	cmd = exec.Command("systemd-run", args...)
	cmd.Env = d.env()
	if out2, err2 := cmd.CombinedOutput(); err2 == nil {
		return nil
	} else {
		log.Printf("systemd-run %s: %v %s / %v %s; falling back to setsid", name, err, out, err2, out2)
	}
	logf, err := os.OpenFile(filepath.Join(studio, "out/claude/launcher.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	c := exec.Command(paint, studio)
	c.Dir = studio
	c.Env = env
	c.Stdout, c.Stderr = logf, logf
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	go c.Wait()
	return nil
}

// Stop interrupts the studio's claude; paint then writes the reply and
// closes the easel.
func (d *Daemon) Stop(st *studioState) error {
	painting := d.paintingNow(st)
	d.mu.Lock()
	pid := d.ix.claudes[st.dir]
	tpid := d.ix.timeouts[st.dir]
	d.mu.Unlock()
	if !painting {
		return &httpError{409, "no painter is at the easel"}
	}
	switch {
	case pid > 0:
		syscall.Kill(pid, syscall.SIGINT)
	case tpid > 0:
		syscall.Kill(tpid, syscall.SIGINT)
	default:
		return &httpError{409, "the painter's claude wasn't found"}
	}
	st.mu.Lock()
	st.stopping = true
	st.mu.Unlock()
	d.Kick()
	return nil
}

// runJob runs a finish or clip for a studio in the background; auto marks
// one the daemon started by itself (heal.go), which yields to anything
// asked of the studio.
func (d *Daemon) runJob(st *studioState, kind string, auto bool, work func(ctx context.Context) error) error {
	st.easelMu.Lock()
	if err := d.easelBusy(st); err != nil {
		st.easelMu.Unlock()
		return err
	}
	if countChunks(filepath.Join(st.dir, "paintings/lua/painting.lua")) == 0 {
		st.easelMu.Unlock()
		return &httpError{409, "nothing is painted yet"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	st.mu.Lock()
	st.job = &Job{Kind: kind, Started: time.Now().UnixMilli(), Auto: auto}
	st.cancel, st.jobDone = cancel, done
	st.errMsg = ""
	own := st.ownEasel
	st.mu.Unlock()
	// an easel the app opened closes first, so its save is the log's
	if own {
		easelRun(st.dir, []string{"close"}, "", waitOther)
		st.mu.Lock()
		st.ownEasel = false
		st.mu.Unlock()
	}
	st.easelMu.Unlock()
	d.Kick()
	go func() {
		err := work(ctx)
		cancelled := ctx.Err() != nil
		cancel()
		st.mu.Lock()
		kind := st.job.Kind // a finish may have turned into its clip
		st.job, st.cancel, st.jobDone = nil, nil, nil
		switch {
		case cancelled:
			log.Printf("%s %s: cancelled", kind, st.name)
		case err != nil:
			st.errMsg = err.Error()
			log.Printf("%s %s: %v", kind, st.name, err)
		}
		if err != nil && !cancelled {
			what := kind
			if auto {
				what = "heal " + kind
			}
			d.Errors.Add("daemon", "error", st.name, what, err.Error(), nil)
		}
		st.mu.Unlock()
		// a job killed from outside (a signal the daemon didn't send: someone
		// stopping a stray process, a restart) says nothing about the
		// painting: the heal tries it again rather than wait for the log
		if auto && err != nil && !cancelled && !killedRe.MatchString(err.Error()) {
			healFailed(st.dir, kind, err)
		}
		close(done)
		d.Kick()
	}()
	return nil
}

// yieldAuto cancels the studio's automatic job, if one runs, and waits for
// it to end, so what was asked can go ahead. Called with st.mu unlocked.
func (st *studioState) yieldAuto() {
	st.mu.Lock()
	if st.job == nil || !st.job.Auto || st.cancel == nil {
		st.mu.Unlock()
		return
	}
	cancel, done := st.cancel, st.jobDone
	st.mu.Unlock()
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
	}
}

// script runs one of the repo's scripts for a studio, its output kept in
// out/app/<logName>; cancelling ctx stops it and everything it started
// (cargo, the replay easel, ffmpeg): it runs in a process group of its own.
func (d *Daemon) script(ctx context.Context, st *studioState, logName string, name string, args ...string) error {
	os.MkdirAll(filepath.Join(st.dir, "out/app"), 0o755)
	cmd := exec.CommandContext(ctx, filepath.Join(d.Repo, "scripts", name), args...)
	cmd.Dir = d.Repo
	cmd.Env = d.env("TMPDIR=" + os.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 15 * time.Second
	out, err := cmd.CombinedOutput()
	os.WriteFile(filepath.Join(st.dir, "out/app", logName), out, 0o644)
	if err != nil {
		return errors.New(lastLines(string(out)+"\n"+err.Error(), 12))
	}
	return nil
}

// FinishReq is POST …/finish.
type FinishReq struct {
	Varnish *bool    `json:"varnish"`
	Coats   *float64 `json:"coats"`
	Cracks  *bool    `json:"cracks"`
	Relief  bool     `json:"relief"`
	// Replay, when above 0, films the painting too once it is finished: the
	// clip's length in seconds, run as the same job's second half (the
	// studio reads "replaying" from then on), so a finished painting comes
	// with its movie
	Replay float64 `json:"replay"`
}

func (d *Daemon) Finish(st *studioState, r FinishReq) error {
	st.yieldAuto()
	args, err := finishArgs(st, r)
	if err != nil {
		return err
	}
	if r.Replay != 0 && (r.Replay < 5 || r.Replay > 600) {
		return &httpError{400, "replay: 5 to 600 seconds"}
	}
	return d.finish(st, r, args, false)
}

func finishArgs(st *studioState, r FinishReq) ([]string, error) {
	args := []string{filepath.Join(st.dir, "paintings/lua/painting.lua"), filepath.Join(st.dir, "out/final.png")}
	if r.Varnish != nil && !*r.Varnish {
		args = append(args, "--no-varnish")
	} else if r.Coats != nil {
		if *r.Coats <= 0 || *r.Coats > 10 {
			return nil, &httpError{400, "coats: more than 0, at most 10"}
		}
		args = append(args, "--coats", fmt.Sprint(*r.Coats))
	}
	if r.Cracks != nil && !*r.Cracks {
		args = append(args, "--no-cracks")
	}
	if r.Relief {
		args = append(args, "--relief")
	}
	return args, nil
}

// finish runs finish_painting (and the clip after it, when r.Replay asks)
// as a job; the options are kept in out/app/finish.json, so a heal of a
// picture gone stale finishes it the way it was finished last.
func (d *Daemon) finish(st *studioState, r FinishReq, args []string, auto bool) error {
	return d.runJob(st, "finish", auto, func(ctx context.Context) error {
		tmp := filepath.Join(st.dir, "out/final.png")
		if err := d.script(ctx, st, "finish.log", "finish_painting", args...); err != nil {
			return err
		}
		if !auto {
			keep := r
			keep.Replay = 0
			if b, err := json.Marshal(keep); err == nil {
				os.WriteFile(filepath.Join(st.dir, "out/app/finish.json"), b, 0o644)
			}
		}
		if err := writeJPEG(tmp, filepath.Join(st.dir, "out/final.jpg"), 1600, 88); err != nil {
			return err
		}
		if r.Replay == 0 {
			return nil
		}
		st.mu.Lock()
		st.job.Kind, st.job.Started = "clip", time.Now().UnixMilli()
		st.mu.Unlock()
		d.Kick()
		return d.clip(ctx, st, r.Replay)
	})
}

// ClipReq is POST …/clip.
type ClipReq struct {
	Length float64 `json:"length"`
}

func (d *Daemon) Clip(st *studioState, r ClipReq) error {
	if r.Length == 0 {
		r.Length = 75
	}
	if r.Length < 5 || r.Length > 600 {
		return &httpError{400, "length: 5 to 600 seconds"}
	}
	st.yieldAuto()
	if b, err := json.Marshal(r); err == nil {
		os.MkdirAll(filepath.Join(st.dir, "out/app"), 0o755)
		os.WriteFile(filepath.Join(st.dir, "out/app/clip.json"), b, 0o644)
	}
	return d.runJob(st, "clip", false, func(ctx context.Context) error { return d.clip(ctx, st, r.Length) })
}

// clip films the painting being made again: replay_clip into out/replay.mp4.
func (d *Daemon) clip(ctx context.Context, st *studioState, length float64) error {
	r := ClipReq{Length: length}
	{
		frames := filepath.Join(st.dir, "out/app/frames")
		os.RemoveAll(frames)
		os.MkdirAll(filepath.Join(st.dir, "out/app"), 0o755)
		// the same replay draws the views (views.go), stamped with the log as it is now
		stamp := logStamp(st.dir)
		os.RemoveAll(viewsNewDir(st))
		args := func(length float64, more ...string) []string {
			return append([]string{filepath.Join(st.dir, "paintings/lua/painting.lua"), filepath.Join(st.dir, "out/replay.mp4"),
				"--length", fmt.Sprint(length), "--sheet", filepath.Join(st.dir, "out/replay-sheet.jpg"),
				"--frames-dir", frames, "--views", viewsNewDir(st)}, more...)
		}
		err := d.script(ctx, st, "clip.log", "replay_clip", args(r.Length)...)
		// a short painting can't fill the length asked: the script names the
		// most it can run, and the frames it replayed are cut again at that
		if m := clipMaxRe.FindStringSubmatch(errString(err)); m != nil {
			if most, perr := strconv.ParseFloat(m[1], 64); perr == nil && most > 0 && most < r.Length {
				err = d.script(ctx, st, "clip.log", "replay_clip", args(most, "--reuse")...)
			}
		}
		// a movie too short to cut still drew its views: keep them either way
		if exists(filepath.Join(viewsNewDir(st), "palette.png")) {
			keepViews(st, stamp)
		}
		return err
	}
}

// Delete moves the studio into .trash.
func (d *Daemon) Delete(st *studioState) error {
	st.yieldAuto()
	st.easelMu.Lock()
	defer st.easelMu.Unlock()
	if d.paintingNow(st) {
		return &httpError{409, "a painter is at the easel"}
	}
	st.mu.Lock()
	if st.job != nil {
		st.mu.Unlock()
		return &httpError{409, "the " + st.job.Kind + " is still running"}
	}
	own := st.ownEasel
	st.mu.Unlock()
	if own || exists(filepath.Join(st.dir, "bin/easel")) {
		easelRun(st.dir, []string{"close"}, "", waitOther)
	}
	if exists(st.dir) {
		trash := filepath.Join(d.Studios, ".trash")
		if err := os.MkdirAll(trash, 0o755); err != nil {
			return err
		}
		if err := os.Rename(st.dir, filepath.Join(trash, fmt.Sprintf("%s.%d", st.name, time.Now().Unix()))); err != nil {
			return err
		}
	}
	d.mu.Lock()
	delete(d.studios, st.name)
	d.mu.Unlock()
	d.Kick()
	return nil
}

// PutBrief replaces BRIEF.md.
func (d *Daemon) PutBrief(st *studioState, text string) error {
	if d.paintingNow(st) {
		return &httpError{409, "a painter is at the easel"}
	}
	st.mu.Lock()
	bad := st.preparing || st.failed
	st.mu.Unlock()
	if bad {
		return &httpError{409, "the studio isn't ready"}
	}
	return os.WriteFile(filepath.Join(st.dir, "BRIEF.md"), []byte(text), 0o644)
}

var killedRe = regexp.MustCompile(`(?m)^signal: (terminated|killed|interrupt)$`)

var clipMaxRe = regexp.MustCompile(`come to at most ([0-9.]+) s`)

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
