package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// a studio whose log holds two chunks, written an hour ago
func healStudio(t *testing.T) string {
	t.Helper()
	dir := makeStudio(t, t.TempDir(), "s")
	logPath := filepath.Join(dir, "paintings/lua/painting.lua")
	os.WriteFile(logPath, []byte("--@ chunk 1\ncanvas{}\n--@ chunk 2\nprint(1)\n"), 0o644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(logPath, old, old)
	return dir
}

func touch(t *testing.T, path string, at time.Time) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("x"), 0o644)
	os.Chtimes(path, at, at)
}

func TestHealNeed(t *testing.T) {
	now := time.Now()
	t.Run("a painter that ended by itself: the picture first", func(t *testing.T) {
		dir := healStudio(t)
		os.WriteFile(filepath.Join(dir, "out/claude/runs.log"), []byte("x start model=m\nx end status=0\n"), 0o644)
		if got := healNeed(dir, 2*time.Minute, now); got != "finish" {
			t.Fatalf("got %q, want finish", got)
		}
		touch(t, filepath.Join(dir, "out/final.png"), now)
		if got := healNeed(dir, 2*time.Minute, now); got != "views" {
			t.Fatalf("with the picture: got %q, want views", got)
		}
		touch(t, filepath.Join(dir, "out/app/views/palette.png"), now)
		os.WriteFile(filepath.Join(dir, "out/app/views", viewsStamp), []byte(logStamp(dir)+"\n"), 0o644)
		if got := healNeed(dir, 2*time.Minute, now); got != "clip" {
			t.Fatalf("with the picture and views: got %q, want clip", got)
		}
		touch(t, filepath.Join(dir, "out/replay.mp4"), now)
		if got := healNeed(dir, 2*time.Minute, now); got != "" {
			t.Fatalf("with all three: got %q, want nothing", got)
		}
	})
	t.Run("a painter stopped, or a hand: views and the replay, no varnish", func(t *testing.T) {
		dir := healStudio(t)
		os.WriteFile(filepath.Join(dir, "out/claude/runs.log"), []byte("x start\nx end status=130\n"), 0o644)
		if got := healNeed(dir, 2*time.Minute, now); got != "views" {
			t.Fatalf("got %q, want views", got)
		}
	})
	t.Run("a painter the daemon stopped exits 0: no varnish", func(t *testing.T) {
		dir := healStudio(t)
		os.WriteFile(filepath.Join(dir, "out/claude/runs.log"), []byte("x start\nx stop\nx end status=0\n"), 0o644)
		if got := healNeed(dir, 2*time.Minute, now); got != "views" {
			t.Fatalf("got %q, want views", got)
		}
		// resumed, and this time it ended by itself
		f, _ := os.OpenFile(filepath.Join(dir, "out/claude/runs.log"), os.O_APPEND|os.O_WRONLY, 0o644)
		f.WriteString("x start model=m --resume s\nx end status=0\n")
		f.Close()
		if got := healNeed(dir, 2*time.Minute, now); got != "finish" {
			t.Fatalf("resumed and ended: got %q, want finish", got)
		}
	})
	t.Run("the log moved on past both", func(t *testing.T) {
		dir := healStudio(t)
		touch(t, filepath.Join(dir, "out/final.png"), now.Add(-2*time.Hour))
		touch(t, filepath.Join(dir, "out/replay.mp4"), now.Add(-2*time.Hour))
		if got := healNeed(dir, 2*time.Minute, now); got != "finish" {
			t.Fatalf("got %q, want finish (a stale picture is made again)", got)
		}
	})
	t.Run("a log still changing waits", func(t *testing.T) {
		dir := healStudio(t)
		logPath := filepath.Join(dir, "paintings/lua/painting.lua")
		os.Chtimes(logPath, now, now)
		if got := healNeed(dir, 2*time.Minute, now); got != "" {
			t.Fatalf("got %q, want nothing yet", got)
		}
	})
	t.Run("nothing painted", func(t *testing.T) {
		dir := makeStudio(t, t.TempDir(), "s")
		if got := healNeed(dir, 0, now); got != "" {
			t.Fatalf("got %q, want nothing", got)
		}
	})
	t.Run("a failure waits for the log to change", func(t *testing.T) {
		dir := healStudio(t)
		healFailed(dir, "views", errors.New("no easel"))
		if got := healNeed(dir, 2*time.Minute, now); got != "clip" {
			t.Fatalf("got %q, want the clip after the views failed on this log", got)
		}
		healFailed(dir, "clip", errors.New("too short to film"))
		if got := healNeed(dir, 2*time.Minute, now); got != "" {
			t.Fatalf("got %q, want nothing after both failed on this log", got)
		}
		logPath := filepath.Join(dir, "paintings/lua/painting.lua")
		os.WriteFile(logPath, []byte("--@ chunk 1\ncanvas{}\n--@ chunk 2\nprint(1)\n--@ chunk 3\nprint(2)\n"), 0o644)
		old := now.Add(-time.Hour)
		os.Chtimes(logPath, old, old)
		if got := healNeed(dir, 2*time.Minute, now); got != "views" {
			t.Fatalf("got %q, want views once the log changed", got)
		}
	})
}

// An automatic job yields: cancelled, it leaves no error and no failure
// record, and the studio is free at once.
func TestAutoJobYields(t *testing.T) {
	d, _, studios, _ := newTestDaemon(t)
	dir := makeStudio(t, studios, "y")
	os.WriteFile(filepath.Join(dir, "paintings/lua/painting.lua"), []byte("--@ chunk 1\ncanvas{}\n"), 0o644)
	st := d.state("y")
	started := make(chan struct{})
	if err := d.runJob(st, "clip", true, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := d.easelBusy(st); err == nil {
		t.Fatal("the studio should read busy while the job runs")
	}
	st.yieldAuto()
	st.mu.Lock()
	job, msg := st.job, st.errMsg
	st.mu.Unlock()
	if job != nil || msg != "" {
		t.Fatalf("after yielding: job %v, error %q", job, msg)
	}
	if _, err := os.Stat(healPath(dir)); err == nil {
		t.Fatal("a cancelled heal must not be recorded as failed")
	}
	if err := d.easelBusy(st); err != nil {
		t.Fatalf("free after yielding: %v", err)
	}
}

// A user's job does not yield.
func TestUserJobDoesNotYield(t *testing.T) {
	d, _, studios, _ := newTestDaemon(t)
	dir := makeStudio(t, studios, "u")
	os.WriteFile(filepath.Join(dir, "paintings/lua/painting.lua"), []byte("--@ chunk 1\ncanvas{}\n"), 0o644)
	st := d.state("u")
	release := make(chan struct{})
	d.runJob(st, "finish", false, func(ctx context.Context) error { <-release; return nil })
	t.Cleanup(func() { close(release) })
	st.yieldAuto()
	st.mu.Lock()
	running := st.job != nil
	st.mu.Unlock()
	if !running {
		t.Fatal("a job the user asked for must not be cancelled")
	}
}

// The kept views answer a look while they are of the log as it is.
func TestKeptViews(t *testing.T) {
	d, srv, studios, _ := newTestDaemon(t)
	dir := makeStudio(t, studios, "v")
	logPath := filepath.Join(dir, "paintings/lua/painting.lua")
	os.WriteFile(logPath, []byte("--@ chunk 1\ncanvas{}\n"), 0o644)
	st := d.state("v")
	nd := viewsNewDir(st)
	os.MkdirAll(nd, 0o755)
	for _, f := range []string{"normal", "value", "squint", "mirror", "relief", "gallery"} {
		writePNG(t, filepath.Join(nd, f+".png"), 50, 30)
	}
	writePNG(t, filepath.Join(nd, "palette.png"), 50, 12)
	if err := keepViews(st, logStamp(dir)); err != nil {
		t.Fatal(err)
	}
	code, m := call(t, "POST", srv.URL+"/v1/studios/v/look", map[string]any{"palette": true})
	if code != 200 || m["h"].(float64) != 12 || exists(filepath.Join(dir, "opens.log")) {
		t.Fatalf("palette from the kept views: %d %v", code, m)
	}
	code, m = call(t, "POST", srv.URL+"/v1/studios/v/look", map[string]any{"mode": "relief"})
	if code != 200 || m["w"].(float64) != 50 {
		t.Fatalf("relief: %d %v", code, m)
	}
	// a crop is the easel's to draw
	_, m = call(t, "POST", srv.URL+"/v1/studios/v/look", map[string]any{"mode": "relief", "crop": "0,0,100,100"})
	if _, opening := m["opening"]; !opening {
		t.Fatalf("a crop should open the easel: %v", m)
	}
	settle(t, st)
	// a log that moved on leaves the views stale
	os.WriteFile(logPath, []byte("--@ chunk 1\ncanvas{}\n--@ chunk 2\nprint(1)\n"), 0o644)
	if viewsFresh(dir) {
		t.Fatal("views of an older log read fresh")
	}
}

// A view asked for on a closed easel without kept views starts drawing them
// (an automatic job) rather than open the easel, and a second ask waits on
// that job instead of cancelling it.
func TestViewStartsTheViewsJob(t *testing.T) {
	d, srv, studios, _ := newTestDaemon(t)
	dir := makeStudio(t, studios, "w")
	os.WriteFile(filepath.Join(dir, "paintings/lua/painting.lua"), []byte("--@ chunk 1\ncanvas{}\n"), 0o644)
	st := d.state("w")
	// hold the job: the test repo has no cargo project, so stand in for it
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	if err := d.runJob(st, "views", true, func(ctx context.Context) error {
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	<-started
	code, m := call(t, "POST", srv.URL+"/v1/studios/w/look", map[string]any{"palette": true})
	if words, _ := m["opening"].(string); code != 200 || !strings.Contains(words, "Drawing the views") {
		t.Fatalf("a palette while the views are drawn: %d %v", code, m)
	}
	st.mu.Lock()
	still := st.job != nil
	st.mu.Unlock()
	if !still || exists(filepath.Join(dir, "opens.log")) {
		t.Fatal("the look cancelled the views job or opened the easel")
	}
	close(release)
}

func TestKilledIsNotAFailure(t *testing.T) {
	if !killedRe.MatchString("some output\nsignal: terminated") || killedRe.MatchString("replay_clip: too short") {
		t.Fatal("killedRe")
	}
}

// The views job takes the studio's own easel's looks: it opens a closed
// easel, keeps every view stamped with the log, and closes it again.
func TestRenderViewsWithTheEasel(t *testing.T) {
	d, _, studios, _ := newTestDaemon(t)
	dir := makeStudio(t, studios, "r")
	os.WriteFile(filepath.Join(dir, "paintings/lua/painting.lua"), []byte("--@ chunk 1\ncanvas{}\n"), 0o644)
	st := d.state("r")
	if err := d.renderViews(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if !viewsFresh(dir) {
		t.Fatal("the views aren't fresh after the job")
	}
	for _, v := range viewLooks {
		if !exists(filepath.Join(dir, "out/app/views", v.file)) {
			t.Fatalf("no %s", v.file)
		}
	}
	if exists(filepath.Join(dir, ".open")) {
		t.Fatal("the job left the easel it opened open")
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "out/easel/painting")); len(entries) != 0 {
		t.Fatalf("looks left in the painter's folder: %d", len(entries))
	}
}

// Finishes start first, then views, then clips; within a kind the newest
// log first.
func TestHealOrder(t *testing.T) {
	now := time.Now()
	st := func(name string) *studioState { return &studioState{name: name} }
	wants := []healWant{
		{st("old-clip"), []string{"clip"}, now.Add(-3 * time.Hour)},
		{st("new-views"), []string{"views", "clip"}, now.Add(-time.Minute)},
		{st("old-finish"), []string{"finish", "views", "clip"}, now.Add(-4 * time.Hour)},
		{st("old-views"), []string{"views"}, now.Add(-2 * time.Hour)},
		{st("new-finish"), []string{"finish"}, now.Add(-2 * time.Minute)},
	}
	healOrder(wants)
	var got []string
	for _, w := range wants {
		got = append(got, w.st.name)
	}
	want := "new-finish old-finish new-views old-views old-clip"
	if strings.Join(got, " ") != want {
		t.Fatalf("order %v, want %s", got, want)
	}
}

// healScripts writes the engine scripts the heal runs, as stand-ins: a
// finish copies the studio's fixture as its picture; a replay says it began
// (clip.started), waits for the studio's views when clip.after-views is
// there, then for clip.release, and writes the movie.
func healScripts(t *testing.T, d *Daemon) {
	t.Helper()
	dir := filepath.Join(d.Engine, "scripts")
	os.MkdirAll(dir, 0o755)
	studio := `studio=$(dirname "$(dirname "$(dirname "$1")")")` + "\n"
	wait := func(f, words string) string {
		return `i=0; while [ ! -f ` + f + ` ] && [ $i -lt 100 ]; do sleep 0.1; i=$((i+1)); done; [ -f ` + f + ` ] || { echo "` + words + `" >&2; exit 1; }` + "\n"
	}
	os.WriteFile(filepath.Join(dir, "finish_painting"), []byte("#!/bin/sh\n"+studio+`cp "$studio/fixture.png" "$2"`+"\n"), 0o755)
	os.WriteFile(filepath.Join(dir, "replay_clip"), []byte("#!/bin/sh\n"+studio+
		`touch "$studio/clip.started"`+"\n"+
		`if [ -f "$studio/clip.after-views" ]; then `+wait(`"$studio/out/app/views/`+viewsStamp+`"`, "the views never came")+"fi\n"+
		wait(`"$studio/clip.release"`, "never released")+
		`echo movie > "$2"`+"\n"), 0o755)
}

// a stub studio whose painter ended by itself an hour ago, wanting all three
func healReady(t *testing.T, d *Daemon, studios, name string) *studioState {
	t.Helper()
	dir := makeStudio(t, studios, name)
	logPath := filepath.Join(dir, "paintings/lua/painting.lua")
	os.WriteFile(logPath, []byte("--@ chunk 1\ncanvas{}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "out/claude/runs.log"), []byte("x start model=m\nx end status=0\n"), 0o644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(logPath, old, old)
	return d.state(name)
}

func jobOf(st *studioState) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.job == nil {
		return ""
	}
	return st.job.Kind
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 600; i++ { // 15 s: a stub easel gives up on its open after 10
		if ok() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("waited in vain for %s", what)
}

// A heal pass starts every finish, and replays up to HealJobs at once, each
// studio's views and clip side by side.
func TestHealRunsSeveral(t *testing.T) {
	d, _, studios, _ := newTestDaemon(t)
	d.NoHeal, d.HealQuiet, d.HealJobs = false, 0, 2
	healScripts(t, d)
	var sts []*studioState
	for _, n := range []string{"a", "b", "c", "d"} {
		sts = append(sts, healReady(t, d, studios, n))
	}
	t.Cleanup(func() {
		for _, st := range sts {
			st.yieldAuto()
		}
	})
	// a pass as the poll makes it: under pollMu, past the ten seconds
	pass := func() {
		d.pollMu.Lock()
		d.healAt = time.Time{}
		d.heal()
		d.pollMu.Unlock()
	}
	// first pass: all four finish (they don't count against HealJobs)
	pass()
	for _, st := range sts {
		if k := jobOf(st); k != "finish" && !exists(filepath.Join(st.dir, "out/final.jpg")) {
			t.Fatalf("%s: job %q after the first pass, want its finish", st.name, k)
		}
	}
	for _, st := range sts {
		waitFor(t, st.name+"'s picture", func() bool { return jobOf(st) == "" && exists(filepath.Join(st.dir, "out/final.jpg")) })
	}
	// second pass: two replays, no more
	pass()
	running := 0
	for _, st := range sts {
		waitFor(t, st.name+"'s views", func() bool { k := jobOf(st); return k == "" || k == "clip" })
		if jobOf(st) == "clip" {
			running++
			if !viewsFresh(st.dir) {
				t.Fatalf("%s films without its views", st.name)
			}
		}
	}
	if running != 2 {
		t.Fatalf("%d replays at once, want HealJobs (2)", running)
	}
	// a third pass with both slots taken starts nothing
	pass()
	n := 0
	for _, st := range sts {
		if jobOf(st) != "" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d jobs after a full pass, want 2", n)
	}
	// released, the two film; the next pass takes the other two
	for _, st := range sts {
		os.WriteFile(filepath.Join(st.dir, "clip.release"), nil, 0o644)
	}
	for _, st := range sts {
		if exists(filepath.Join(st.dir, "clip.started")) {
			waitFor(t, st.name+"'s movie", func() bool { return jobOf(st) == "" && exists(filepath.Join(st.dir, "out/replay.mp4")) })
		}
	}
	pass()
	for _, st := range sts {
		waitFor(t, st.name+"'s movie", func() bool { return jobOf(st) == "" && exists(filepath.Join(st.dir, "out/replay.mp4")) })
		if need := healNeed(st.dir, 0, time.Now()); need != "" {
			t.Fatalf("%s still wants %q", st.name, need)
		}
	}
}

// The views and the clip of one studio run at once: here each waits for the
// other (the easel opens only once the replay has begun, the replay ends
// only once the views are kept), so one after the other would fail. The
// job reads views, then clip.
func TestViewsAndClipSideBySide(t *testing.T) {
	d, _, studios, _ := newTestDaemon(t)
	healScripts(t, d)
	st := healReady(t, d, studios, "s")
	touch(t, filepath.Join(st.dir, "out/final.png"), time.Now())
	os.WriteFile(filepath.Join(st.dir, "open.waits"), []byte("clip.started"), 0o644)
	os.WriteFile(filepath.Join(st.dir, "clip.after-views"), nil, 0o644)
	needs := healNeeds(st.dir, 0, time.Now())
	if strings.Join(needs, " ") != "views clip" {
		t.Fatalf("needs %v", needs)
	}
	if err := d.startHeal(st, needs); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.yieldAuto)
	waitFor(t, "the job to turn to its clip", func() bool { return jobOf(st) == "clip" })
	if !viewsFresh(st.dir) {
		t.Fatal("the job turned to its clip before the views were kept")
	}
	os.WriteFile(filepath.Join(st.dir, "clip.release"), nil, 0o644)
	waitFor(t, "the movie", func() bool { return jobOf(st) == "" && exists(filepath.Join(st.dir, "out/replay.mp4")) })
	st.mu.Lock()
	msg := st.errMsg
	st.mu.Unlock()
	if msg != "" || exists(healPath(st.dir)) {
		t.Fatalf("error %q, heal.json %v", msg, exists(healPath(st.dir)))
	}
}

// Views that fail are their own failure; the clip beside them still films.
func TestViewsFailClipFilms(t *testing.T) {
	d, _, studios, _ := newTestDaemon(t)
	healScripts(t, d)
	st := healReady(t, d, studios, "f")
	touch(t, filepath.Join(st.dir, "out/final.png"), time.Now())
	os.WriteFile(filepath.Join(st.dir, "open.waits"), []byte("never"), 0o644) // the easel won't open
	os.WriteFile(filepath.Join(st.dir, "clip.release"), nil, 0o644)
	if err := d.startHeal(st, []string{"views", "clip"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.yieldAuto)
	waitFor(t, "the job to end", func() bool { return jobOf(st) == "" && exists(filepath.Join(st.dir, "out/replay.mp4")) })
	st.mu.Lock()
	msg := st.errMsg
	st.mu.Unlock()
	failed := readHeal(st.dir)
	if !strings.Contains(msg, "didn't open") || failed["views"].Log == "" || failed["clip"].Log != "" {
		t.Fatalf("error %q, heal.json %v", msg, failed)
	}
	if need := healNeed(st.dir, 0, time.Now()); need != "" {
		t.Fatalf("still wants %q after the views failed on this log and the clip was made", need)
	}
}

// Stop says so in runs.log before it signals claude, so the heal doesn't
// varnish a painting stopped to be resumed (claude interrupted exits 0).
func TestStopIsInRunsLog(t *testing.T) {
	d, srv, studios, fp := newTestDaemon(t)
	dir := healStudio(t)
	os.Rename(dir, filepath.Join(studios, "p"))
	dir = filepath.Join(studios, "p")
	runs := filepath.Join(dir, "out/claude/runs.log")
	os.WriteFile(runs, []byte("2026-10-09T14:50:26Z start model=m effort=high hours=5 \n"), 0o644)
	claude := exec.Command("sleep", "30") // stands in for claude: the SIGINT lands here
	if err := claude.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { claude.Process.Kill() })
	fp.set(Proc{PID: 10, Comm: "bash", Argv: []string{"bash", "/x/harness/claude/paint", dir}, Cwd: dir},
		Proc{PID: claude.Process.Pid, Comm: "claude", Argv: []string{"claude", "-p"}, Cwd: dir})
	d.state("p")
	if code, m := call(t, "POST", srv.URL+"/v1/studios/p/stop", nil); code != 200 {
		t.Fatalf("stop: %d %v", code, m)
	}
	if err := claude.Wait(); err == nil || !strings.Contains(err.Error(), "interrupt") {
		t.Fatalf("claude: %v, want interrupted", err)
	}
	b, _ := os.ReadFile(runs)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ stop$`).MatchString(lines[1]) {
		t.Fatalf("runs.log %q", lines)
	}
	// paint's end, as claude interrupted leaves it
	f, _ := os.OpenFile(runs, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("2026-10-09T14:51:03Z end status=0\n")
	f.Close()
	fp.set()
	if lastRunClean(dir) || slices.Contains(healNeeds(dir, 0, time.Now()), "finish") {
		t.Fatal("a stopped painter's painting is to be varnished")
	}
	if ri := readRuns(runs); ri.ended == nil || ri.exit == nil || *ri.exit != 0 {
		t.Fatalf("runs: %+v", ri)
	}
}

// A painting too short to film is no failure of the heal: it is recorded
// against the log (not tried again until it changes), the studio reads
// short, and nothing goes to the error log or the studio's error.
func TestTooShortToFilm(t *testing.T) {
	d, _, studios, _ := newTestDaemon(t)
	healScripts(t, d)
	os.WriteFile(filepath.Join(d.Engine, "scripts", "replay_clip"), []byte(`#!/bin/sh
for a; do [ "$a" = --reuse ] && { echo "replay_clip: --length must exceed the 3 s final hold" >&2; exit 1; }; done
echo "replay_clip: --length 75 is longer than the clip can run: 0 moments held at most 4 s each (--max-hold) plus the 3 s final hold come to at most 3.00 s; " >&2
exit 1
`), 0o755)
	st := healReady(t, d, studios, "t")
	touch(t, filepath.Join(st.dir, "out/final.png"), time.Now())
	if err := d.startHeal(st, []string{"clip"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the clip to end", func() bool { return jobOf(st) == "" })
	st.mu.Lock()
	msg := st.errMsg
	st.mu.Unlock()
	if logged := d.Errors.Tail(10); msg != "" || len(logged) != 0 {
		t.Fatalf("error %q, error log %s", msg, logged)
	}
	if readHeal(st.dir)["clip"].Error != errTooShort.Error() || slices.Contains(healNeeds(st.dir, 0, time.Now()), "clip") {
		t.Fatalf("heal.json %v", readHeal(st.dir))
	}
	if o := d.object(st); !o.Short || o.Error != "" {
		t.Fatalf("studio short %v error %q", o.Short, o.Error)
	}
	// asked for by hand, the words are the studio's error
	if err := d.runJob(st, "clip", false, func(ctx context.Context) error { return d.clip(ctx, st, 75) }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the asked clip to end", func() bool { return jobOf(st) == "" })
	if o := d.object(st); o.Error != errTooShort.Error() {
		t.Fatalf("asked: error %q", o.Error)
	}
}

// A board with no piles is a view like the others: the views are kept, and
// a palette look answers the easel's words as the easel would.
func TestViewsOfABarePalette(t *testing.T) {
	d, srv, studios, _ := newTestDaemon(t)
	dir := makeStudio(t, studios, "b")
	os.WriteFile(filepath.Join(dir, "paintings/lua/painting.lua"), []byte("--@ chunk 1\ncanvas{}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "bin/easel"), []byte(strings.Replace(stubEasel, "look) ",
		`look) case "$*" in *--palette*) echo "look --palette: no piles on the palette yet" >&2; exit 1 ;; esac; `, 1)), 0o755)
	st := d.state("b")
	if err := d.renderViews(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if !viewsFresh(dir) || healNeed(dir, 0, time.Now().Add(time.Hour)) == "views" {
		t.Fatal("the views of a bare palette aren't kept")
	}
	opens := countLines(filepath.Join(dir, "opens.log")) // the views job's own
	code, m := easelCall(t, srv.URL+"/v1/studios/b/look", map[string]any{"palette": true})
	if code != 422 || !strings.Contains(fmt.Sprint(m["error"]), "no piles on the palette") {
		t.Fatalf("palette look: %d %v", code, m)
	}
	if code, m = easelCall(t, srv.URL+"/v1/studios/b/look", map[string]any{"mode": "value"}); code != 200 || m["w"] == nil {
		t.Fatalf("value look: %d %v", code, m)
	}
	if countLines(filepath.Join(dir, "opens.log")) != opens {
		t.Fatal("a kept view opened the easel")
	}
}

// Views that fail leave no views.new behind.
func TestFailedViewsLeaveNothing(t *testing.T) {
	d, _, studios, _ := newTestDaemon(t)
	dir := makeStudio(t, studios, "n")
	os.WriteFile(filepath.Join(dir, "paintings/lua/painting.lua"), []byte("--@ chunk 1\ncanvas{}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "bin/easel"), []byte(strings.Replace(stubEasel, "look) ",
		`look) case "$*" in *relief*) echo "relief: boom" >&2; exit 1 ;; esac; `, 1)), 0o755)
	st := d.state("n")
	if err := d.renderViews(context.Background(), st); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err %v", err)
	}
	if exists(viewsNewDir(st)) {
		t.Fatal("views.new left behind")
	}
}

func countLines(path string) int {
	b, _ := os.ReadFile(path)
	return strings.Count(string(b), "\n")
}
