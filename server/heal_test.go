package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
		if got := healNeed(dir, 2*time.Minute, now); got != "clip" {
			t.Fatalf("with the picture: got %q, want clip", got)
		}
		touch(t, filepath.Join(dir, "out/replay.mp4"), now)
		if got := healNeed(dir, 2*time.Minute, now); got != "views" {
			t.Fatalf("with both: got %q, want views", got)
		}
		touch(t, filepath.Join(dir, "out/app/views/palette.png"), now)
		os.WriteFile(filepath.Join(dir, "out/app/views", viewsStamp), []byte(logStamp(dir)+"\n"), 0o644)
		if got := healNeed(dir, 2*time.Minute, now); got != "" {
			t.Fatalf("with all three: got %q, want nothing", got)
		}
	})
	t.Run("a painter stopped, or a hand: the replay only", func(t *testing.T) {
		dir := healStudio(t)
		os.WriteFile(filepath.Join(dir, "out/claude/runs.log"), []byte("x start\nx end status=130\n"), 0o644)
		if got := healNeed(dir, 2*time.Minute, now); got != "clip" {
			t.Fatalf("got %q, want clip", got)
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
		healFailed(dir, "clip", errors.New("too short to film"))
		if got := healNeed(dir, 2*time.Minute, now); got != "views" {
			t.Fatalf("got %q, want the views after the clip failed on this log", got)
		}
		healFailed(dir, "views", errors.New("no"))
		if got := healNeed(dir, 2*time.Minute, now); got != "" {
			t.Fatalf("got %q, want nothing after both failed on this log", got)
		}
		logPath := filepath.Join(dir, "paintings/lua/painting.lua")
		os.WriteFile(logPath, []byte("--@ chunk 1\ncanvas{}\n--@ chunk 2\nprint(1)\n--@ chunk 3\nprint(2)\n"), 0o644)
		old := now.Add(-time.Hour)
		os.Chtimes(logPath, old, old)
		if got := healNeed(dir, 2*time.Minute, now); got != "clip" {
			t.Fatalf("got %q, want clip once the log changed", got)
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

func TestLookIn(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "mytool")
	os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755)
	env := []string{"HOME=/x", "PATH=/nowhere:" + dir}
	if got := lookIn(env, "mytool"); got != tool {
		t.Fatalf("lookIn = %q, want %q", got, tool)
	}
	if got := lookIn(env, "/bin/sh"); got != "/bin/sh" {
		t.Fatalf("a path stays: %q", got)
	}
	if got := lookIn(env, "nothere"); got != "nothere" {
		t.Fatalf("not found stays: %q", got)
	}
	if !killedRe.MatchString("some output\nsignal: terminated") || killedRe.MatchString("replay_clip: too short") {
		t.Fatal("killedRe")
	}
}
