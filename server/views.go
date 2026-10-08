package main

// The views: the finished canvas as each of `look`'s modes shows it, and the
// palette board, kept in out/app/views/ with the stamp of the log they were
// drawn from. A view asked for on a closed easel would otherwise wait for an
// open, which replays the whole log (the save holds the canvas and the
// piles, not the board); with the views kept, the app's View menu answers at
// once. They come from `easel run --views` (crates/easel), as part of the
// replay a clip makes anyway, or as a heal of their own (heal.go) for a
// painting whose views are missing or older than its log.

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// viewFiles: the files `easel run --views` writes, by the app's name for each view.
var viewFiles = map[string]string{
	"":        "normal.png",
	"normal":  "normal.png",
	"value":   "value.png",
	"squint":  "squint.png",
	"mirror":  "mirror.png",
	"relief":  "relief.png",
	"gallery": "gallery.png",
	"palette": "palette.png",
}

const viewsStamp = "log.stamp"

func viewsDir(st *studioState) string    { return filepath.Join(st.dir, "out/app/views") }
func viewsNewDir(st *studioState) string { return filepath.Join(st.dir, "out/app/views.new") }
func logStamp(dir string) string {
	s, _, _ := stampOf(filepath.Join(dir, "paintings/lua/painting.lua"))
	return s
}

// viewsFresh: the kept views were drawn from the log as it is now.
func viewsFresh(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "out/app/views", viewsStamp))
	return err == nil && strings.TrimSpace(string(b)) == logStamp(dir) && exists(filepath.Join(dir, "out/app/views/palette.png"))
}

// keepViews moves a finished views.new into place, stamped with the log it
// was drawn from (taken before the replay began).
func keepViews(st *studioState, stamp string) error {
	nd := viewsNewDir(st)
	if !exists(filepath.Join(nd, "palette.png")) {
		return errors.New("the replay drew no views")
	}
	os.Remove(filepath.Join(nd, "final.png"))
	if err := os.WriteFile(filepath.Join(nd, viewsStamp), []byte(stamp+"\n"), 0o644); err != nil {
		return err
	}
	os.RemoveAll(viewsDir(st))
	return os.Rename(nd, viewsDir(st))
}

// renderViews replays the log with the replay build to draw the views: the
// heal for a painting whose replay was made before views were.
func (d *Daemon) renderViews(ctx context.Context, st *studioState) error {
	stamp := logStamp(st.dir)
	nd := viewsNewDir(st)
	os.RemoveAll(nd)
	os.Remove(filepath.Join(st.dir, "out/app/views.log"))
	// the replay build, as finish_painting and replay_clip use it (cargo
	// answers at once when it is up to date)
	if err := d.command(ctx, st, "views.log", "cargo", "build", "--release", "-p", "easel"); err != nil {
		return err
	}
	easel := filepath.Join(d.Repo, "target/release/easel")
	if err := d.command(ctx, st, "views.log", easel, "run", filepath.Join(st.dir, "paintings/lua/painting.lua"),
		"--out", filepath.Join(nd, "final.png"), "--views", nd); err != nil {
		return err
	}
	return keepViews(st, stamp)
}

// command runs a program for a studio from the repo, stopped with
// everything it started when ctx ends. Its output goes to out/app/<logName>
// as it comes (appended: a job's steps share one log), so a look can tell
// how far a replay has got.
func (d *Daemon) command(ctx context.Context, st *studioState, logName string, name string, args ...string) error {
	os.MkdirAll(filepath.Join(st.dir, "out/app"), 0o755)
	logPath := filepath.Join(st.dir, "out/app", logName)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	start, _ := f.Seek(0, 2)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = d.Repo
	cmd.Env = d.env("TMPDIR=" + os.TempDir())
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 15 * time.Second
	if err := cmd.Run(); err != nil {
		b, _ := os.ReadFile(logPath)
		if int64(len(b)) > start {
			b = b[start:]
		}
		return errors.New(lastLines(string(b)+"\n"+err.Error(), 12))
	}
	return nil
}

// replayChunkRe: the line `easel run` writes after each chunk it replays.
var replayChunkRe = regexp.MustCompile(`(?m)^\s*chunk\s+(\d+)\s`)

// viewsProgress: how far the replay drawing the views has got, from the
// newest of the views job's log and the clip's replay log (frames.log).
func viewsProgress(st *studioState) string {
	var newest []byte
	var at time.Time
	for _, rel := range []string{"out/app/views.log", "out/app/frames.log"} {
		p := filepath.Join(st.dir, rel)
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(at) {
			if b, err := os.ReadFile(p); err == nil {
				newest, at = b, fi.ModTime()
			}
		}
	}
	st.mu.Lock()
	total := st.chunks
	st.mu.Unlock()
	if ms := replayChunkRe.FindAllSubmatch(newest, -1); len(ms) > 0 && total > 0 {
		return fmt.Sprintf("Drawing the views from a replay of the painting: chunk %s of %d…", ms[len(ms)-1][1], total)
	}
	return "Drawing the views from a replay of the painting…"
}

// isView: a look the kept views can answer (one of them whole).
func isView(r LookReq) bool {
	if r.Crop != "" || r.Size != 0 || r.Light != "" || r.Grid != nil || r.Scratch || r.Survey {
		return false
	}
	if r.Palette {
		return r.Mode == ""
	}
	_, ok := viewFiles[strings.TrimSpace(r.Mode)]
	return ok
}

// viewsComing: for a view the kept views can't answer yet, the words to
// answer while they are being drawn — by a heal already at it, or by one
// started now — so a look never cancels the job that would answer it, and
// a closed easel isn't opened (a replay as long as the views' own, kept for
// nothing). "" when the easel is open or busy otherwise: the look goes there.
func (d *Daemon) viewsComing(st *studioState) string {
	st.mu.Lock()
	job, own := st.job, st.ownEasel || st.opening
	st.mu.Unlock()
	if job != nil && job.Auto && (job.Kind == "views" || job.Kind == "clip") {
		return viewsProgress(st)
	}
	if job != nil || own || exists(filepath.Join(st.dir, "out/easel/painting/sock")) || d.paintingNow(st) {
		return ""
	}
	if err := d.runJob(st, "views", true, func(ctx context.Context) error { return d.renderViews(ctx, st) }); err != nil {
		return ""
	}
	return "Drawing the views from a replay of the painting…"
}

// keptView answers a look from the kept views when they are of the log as
// it is and the look is one of them whole: no crop, size, light, grid,
// scratch or survey. nil otherwise.
func (d *Daemon) keptView(st *studioState, r LookReq) *LookResult {
	if r.Crop != "" || r.Size != 0 || r.Light != "" || r.Grid != nil || r.Scratch || r.Survey {
		return nil
	}
	name := r.Mode
	if r.Palette {
		if name != "" {
			return nil
		}
		name = "palette"
	}
	file, ok := viewFiles[strings.TrimSpace(name)]
	if !ok || !viewsFresh(st.dir) {
		return nil
	}
	src := filepath.Join(viewsDir(st), file)
	f, err := os.Open(src)
	if err != nil {
		return nil
	}
	cfg, _, err := image.DecodeConfig(f)
	f.Close()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return nil
	}
	rel := "out/app/looks/" + newID() + ".png"
	os.MkdirAll(filepath.Join(st.dir, "out/app/looks"), 0o755)
	if os.WriteFile(filepath.Join(st.dir, rel), b, 0o644) != nil {
		return nil
	}
	return &LookResult{Said: rel + fmt.Sprintf(" (%dx%d)", cfg.Width, cfg.Height) + "\n(drawn from the finished painting's replay)",
		Images: []string{rel}, W: cfg.Width, H: cfg.Height}
}
