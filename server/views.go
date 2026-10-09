package main

// The views: the finished canvas as each of `look`'s modes shows it, and the
// palette board, kept in out/app/views/ with the stamp of the log they were
// drawn from. A view asked for on a closed easel would otherwise wait for an
// open, which replays the whole log (the save holds the canvas and the
// piles, not the board); with the views kept, the app's View menu answers at
// once. They are the studio's own easel's looks, taken by a views job
// (heal.go): it opens the easel (that one replay), looks in every mode and at
// the palette, keeps them, and closes it again. The engine stays as upstream
// publishes it.

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

// viewFiles: the files the views job keeps, by the app's name for each view.
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

// paletteNone stands in for palette.png when the board has no piles: the
// easel's own words, answered as the easel would answer them (a 422).
const paletteNone = "palette.txt"

var noPilesRe = regexp.MustCompile(`no piles on the palette`)

func viewsDir(st *studioState) string    { return filepath.Join(st.dir, "out/app/views") }
func viewsNewDir(st *studioState) string { return filepath.Join(st.dir, "out/app/views.new") }
func logStamp(dir string) string {
	s, _, _ := stampOf(filepath.Join(dir, "paintings/lua/painting.lua"))
	return s
}

// hasPalette: a views folder holds the palette, or the words for a board
// with nothing on it.
func hasPalette(dir string) bool {
	return exists(filepath.Join(dir, "palette.png")) || exists(filepath.Join(dir, paletteNone))
}

// viewsFresh: the kept views were drawn from the log as it is now.
func viewsFresh(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "out/app/views", viewsStamp))
	return err == nil && strings.TrimSpace(string(b)) == logStamp(dir) && hasPalette(filepath.Join(dir, "out/app/views"))
}

// keepViews moves a finished views.new into place, stamped with the log it
// was drawn from (taken before the replay began).
func keepViews(st *studioState, stamp string) error {
	nd := viewsNewDir(st)
	if !hasPalette(nd) {
		return errors.New("the replay drew no views")
	}
	os.Remove(filepath.Join(nd, "final.png"))
	if err := os.WriteFile(filepath.Join(nd, viewsStamp), []byte(stamp+"\n"), 0o644); err != nil {
		return err
	}
	os.RemoveAll(viewsDir(st))
	return os.Rename(nd, viewsDir(st))
}

// viewLooks: the views job's looks, by file: the easel's own arguments.
var viewLooks = []struct {
	file string
	args []string
}{
	{"normal.png", []string{"look"}},
	{"value.png", []string{"look", "--mode", "value"}},
	{"squint.png", []string{"look", "--mode", "squint"}},
	{"mirror.png", []string{"look", "--mode", "mirror"}},
	{"relief.png", []string{"look", "--mode", "relief"}},
	{"gallery.png", []string{"look", "--mode", "gallery"}},
	{"palette.png", []string{"look", "--palette"}},
}

// renderViews draws the views with the studio's easel: opened if it is
// closed (a replay of the whole log, cancelled with ctx), every view looked
// at, and closed again if this job opened it. A job cancelled while the
// easel is open leaves it open for whoever asked (the daemon's, closed when
// idle), since that is what they came for.
func (d *Daemon) renderViews(ctx context.Context, st *studioState) (err error) {
	stamp := logStamp(st.dir)
	nd := viewsNewDir(st)
	os.RemoveAll(nd)
	if err := os.MkdirAll(nd, 0o755); err != nil {
		return err
	}
	// views half drawn are no use to anyone
	defer func() {
		if err != nil {
			os.RemoveAll(nd)
		}
	}()
	opened := false
	if _, code := easelRunCtx(ctx, st.dir, []string{"status"}, 20*time.Second); code != 0 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		out, code := easelRunCtx(ctx, st.dir, []string{"open"}, waitOpen)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if code != 0 {
			return errors.New("the easel didn't open: " + lastLines(out, 6))
		}
		opened = true
	}
	leave := func() {
		if ctx.Err() != nil {
			st.mu.Lock()
			st.ownEasel, st.lastUse = true, time.Now()
			st.mu.Unlock()
		} else if opened {
			easelRun(st.dir, []string{"close"}, "", waitOther)
		}
	}
	defer leave()
	for _, v := range viewLooks {
		out, code := easelRunCtx(ctx, st.dir, v.args, waitOther)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var src string
		for _, ln := range strings.Split(out, "\n") {
			if m := pngRe.FindStringSubmatch(strings.TrimSpace(ln)); m != nil {
				src = m[1]
			}
		}
		// a painting with nothing mixed has a bare board: that is its view
		if v.file == "palette.png" && code != 0 && noPilesRe.MatchString(out) {
			if err := os.WriteFile(filepath.Join(nd, paletteNone), []byte(strings.TrimSpace(out)+"\n"), 0o644); err != nil {
				return err
			}
			continue
		}
		if code != 0 || src == "" {
			return fmt.Errorf("the easel's %s: %s", strings.Join(v.args, " "), lastLines(out, 4))
		}
		if !filepath.IsAbs(src) {
			src = filepath.Join(st.dir, src)
		}
		if err := os.Rename(src, filepath.Join(nd, v.file)); err != nil {
			return err
		}
	}
	return keepViews(st, stamp)
}

// easelRunCtx is easelRun that a cancelled ctx stops, with all it started.
func easelRunCtx(ctx context.Context, studio string, args []string, wait time.Duration) (string, int) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(studio, "bin", "easel"), args...)
	cmd.Dir = studio
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + os.Getenv("HOME"), "RAYON_NUM_THREADS=4"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ctx.Err() == nil {
			return string(out), ee.ExitCode()
		}
		return string(out) + err.Error(), -1
	}
	return string(out), 0
}

// viewsProgress: how far the views job has got: the easel's open, replaying
// the log ("resuming chunk k/n" in its server.log), then its looks.
func viewsProgress(st *studioState) string {
	words := openProgress(st.dir)
	if strings.HasPrefix(words, "Opening the easel: replaying chunk ") {
		return "Drawing the views: replaying chunk " + strings.TrimPrefix(words, "Opening the easel: replaying chunk ")
	}
	return "Drawing the views…"
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
	if job != nil && job.Auto && job.Kind == "views" {
		return viewsProgress(st)
	}
	// another heal (a replay, a finish) steps aside: a view asked for comes first
	if job != nil && job.Auto {
		st.yieldAuto()
		st.mu.Lock()
		job = st.job
		st.mu.Unlock()
	}
	if job != nil || own || exists(filepath.Join(st.dir, "out/easel/painting/sock")) || d.paintingNow(st) {
		return ""
	}
	if err := d.runJob(st, "views", true, func(ctx context.Context) error { return d.renderViews(ctx, st) }); err != nil {
		return ""
	}
	return "Drawing the views…"
}

// keptView answers a look from the kept views when they are of the log as
// it is and the look is one of them whole: no crop, size, light, grid,
// scratch or survey; a bare board's palette is the easel's 422. nil, nil
// otherwise.
func (d *Daemon) keptView(st *studioState, r LookReq) (*LookResult, error) {
	if r.Crop != "" || r.Size != 0 || r.Light != "" || r.Grid != nil || r.Scratch || r.Survey {
		return nil, nil
	}
	name := r.Mode
	if r.Palette {
		if name != "" {
			return nil, nil
		}
		name = "palette"
	}
	file, ok := viewFiles[strings.TrimSpace(name)]
	if !ok || !viewsFresh(st.dir) {
		return nil, nil
	}
	if file == "palette.png" {
		if b, err := os.ReadFile(filepath.Join(viewsDir(st), paletteNone)); err == nil {
			return nil, &httpError{422, strings.TrimSpace(string(b))}
		}
	}
	src := filepath.Join(viewsDir(st), file)
	f, err := os.Open(src)
	if err != nil {
		return nil, nil
	}
	cfg, _, err := image.DecodeConfig(f)
	f.Close()
	if err != nil {
		return nil, nil
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return nil, nil
	}
	rel := "out/app/looks/" + newID() + ".png"
	os.MkdirAll(filepath.Join(st.dir, "out/app/looks"), 0o755)
	if os.WriteFile(filepath.Join(st.dir, rel), b, 0o644) != nil {
		return nil, nil
	}
	return &LookResult{Said: rel + fmt.Sprintf(" (%dx%d)", cfg.Width, cfg.Height) + "\n(drawn from the finished painting's replay)",
		Images: []string{rel}, W: cfg.Width, H: cfg.Height}, nil
}
