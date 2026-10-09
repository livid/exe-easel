package main

// The app's hand at the easel: look and do on a studio nobody paints in.
// Every call runs the studio's own bin/easel client, as the painter's tools
// do (harness/painter/easel-client.ts): a bare environment, the studio as
// the working folder, and `status` first so a closed easel is opened (it
// replays its log from the save) before the command.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

const (
	waitDo    = 12 * time.Minute
	waitOther = 3 * time.Minute
	waitOpen  = 30 * time.Minute
	idleClose = 10 * time.Minute
)

// easel runs bin/easel in the studio with args, input on stdin. It returns
// stdout and stderr together and the exit status (-1: it didn't finish).
func easelRun(studio string, args []string, input string, wait time.Duration) (string, int) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	cmd := exec.Command(filepath.Join(studio, "bin", "easel"), args...)
	cmd.Dir = studio
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + os.Getenv("HOME"), "RAYON_NUM_THREADS=4"}
	cmd.Stdin = strings.NewReader(input)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return err.Error(), -1
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return out.String(), ee.ExitCode()
			}
			return out.String() + err.Error(), -1
		}
		return out.String(), 0
	case <-ctx.Done():
		syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		<-done
		return "the easel didn't answer in time", -1
	}
}

var rebuildRe = regexp.MustCompile(`^rebuilding from the log \((\d+) of (\d+) chunks\)$`)

// ensureOpen opens the studio's easel if it is closed and waits while it
// rebuilds. It reports whether it opened it.
func ensureOpen(studio string) (bool, error) {
	deadline := time.Now().Add(waitOpen)
	opened := false
	for time.Now().Before(deadline) {
		out, code := easelRun(studio, []string{"status"}, "", waitOther)
		if code == 0 {
			if rebuildRe.MatchString(strings.TrimSpace(out)) {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			return opened, nil
		}
		if opened {
			return opened, fmt.Errorf("the easel didn't open: %s", strings.TrimSpace(out))
		}
		out, code = easelRun(studio, []string{"open"}, "", waitOther)
		if code != 0 {
			return false, fmt.Errorf("the easel didn't open: %s", strings.TrimSpace(out))
		}
		opened = true
	}
	return opened, errors.New("the easel rebuild took too long")
}

// LookReq is POST …/look.
type LookReq struct {
	Mode    string `json:"mode"`
	Crop    string `json:"crop"`
	Size    int    `json:"size"`
	Light   string `json:"light"`
	Grid    any    `json:"grid"`
	Palette bool   `json:"palette"`
	Scratch bool   `json:"scratch"`
	Survey  bool   `json:"survey"`
}

func (r LookReq) args() []string {
	a := []string{"look"}
	if r.Scratch {
		a = append(a, "--scratch")
	}
	if r.Palette {
		return append(a, "--palette")
	}
	if r.Survey {
		a = append(a, "--survey")
	}
	if r.Crop != "" {
		a = append(a, "--crop", r.Crop)
	}
	if r.Mode != "" && r.Mode != "normal" {
		a = append(a, "--mode", r.Mode)
	}
	if r.Light != "" {
		a = append(a, "--light", r.Light)
	}
	if r.Size > 0 {
		a = append(a, "--size", fmt.Sprint(r.Size))
	}
	switch g := r.Grid.(type) {
	case bool:
		if g {
			a = append(a, "--grid")
		}
	case float64:
		if g > 0 {
			a = append(a, "--grid", fmt.Sprint(g))
		}
	}
	return a
}

// whole: the look shows the painting as it hangs.
func (r LookReq) whole() bool {
	return !r.Scratch && !r.Palette && !r.Survey && r.Crop == "" && (r.Mode == "" || r.Mode == "normal" || r.Mode == "gallery")
}

type appLook struct {
	T    int64  `json:"t"`
	Path string `json:"path"`
	W    int    `json:"w"`
	H    int    `json:"h"`
}

// lastAppLook reads the newest entry of out/app/looks.jsonl.
func lastAppLook(studio string) (appLook, bool) {
	b, err := os.ReadFile(filepath.Join(studio, "out/app/looks.jsonl"))
	if err != nil {
		return appLook{}, false
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var l appLook
		if json.Unmarshal([]byte(lines[i]), &l) == nil && l.Path != "" && exists(filepath.Join(studio, l.Path)) {
			return l, true
		}
	}
	return appLook{}, false
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// LookResult is the answer to POST …/look.
type LookResult struct {
	Said   string   `json:"said"`
	Images []string `json:"images"`
	W      int      `json:"w"`
	H      int      `json:"h"`
}

// errBusy: a painter is at the easel, or a job runs.
var errBusy = &httpError{409, "the painter is at the easel"}

func (d *Daemon) easelBusy(st *studioState) error {
	if d.paintingNow(st) {
		return errBusy
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.launched.IsZero() && time.Since(st.launched) < 15*time.Second {
		return errBusy
	}
	if st.preparing || st.failed {
		return &httpError{409, "the studio isn't ready"}
	}
	if st.job != nil {
		return &httpError{409, "the " + st.job.Kind + " is still running"}
	}
	return nil
}

// openingError: the easel is on its way; the app asks again.
type openingError struct{ progress string }

func (e *openingError) Error() string { return e.progress }

// resumeRe: the line the easel's server writes as it replays the log on
// open (out/easel/painting/server.log).
var resumeRe = regexp.MustCompile(`resum(?:ing|ed) chunk (\d+)/(\d+)`)

func openProgress(dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "out/easel/painting/server.log"))
	ms := resumeRe.FindAllSubmatch(b, -1)
	if len(ms) == 0 {
		return "Opening the easel…"
	}
	m := ms[len(ms)-1]
	return fmt.Sprintf("Opening the easel: replaying chunk %s of %s…", m[1], m[2])
}

// easelReady says whether the studio's easel is open for the app. A closed
// one is opened in the background and the call answers at once with how far
// it has got: the save holds only the canvas, so an open replays the whole
// log (ten minutes for a long painting), and `easel open` cut off by a
// clock leaves no easel at all. The app asks again until it is ready.
func (d *Daemon) easelReady(st *studioState) (bool, error) {
	st.mu.Lock()
	opening, openErr := st.opening, st.openErr
	st.openErr = ""
	st.mu.Unlock()
	if opening {
		return false, &openingError{openProgress(st.dir)}
	}
	if openErr != "" {
		return false, &httpError{502, openErr}
	}
	out, code := easelRun(st.dir, []string{"status"}, "", 20*time.Second)
	if code == 0 {
		if m := rebuildRe.FindStringSubmatch(strings.TrimSpace(out)); m != nil {
			return false, &openingError{fmt.Sprintf("The easel is rebuilding: chunk %s of %s…", m[1], m[2])}
		}
		return true, nil
	}
	st.mu.Lock()
	st.opening, st.ownEasel, st.lastUse = true, true, time.Now()
	st.mu.Unlock()
	go func() {
		out, code := easelRun(st.dir, []string{"open"}, "", waitOpen)
		st.mu.Lock()
		st.opening, st.lastUse = false, time.Now()
		if code != 0 {
			st.openErr = "the easel didn't open: " + lastLines(out, 6)
			st.ownEasel = false
		}
		msg := st.openErr
		st.mu.Unlock()
		if code != 0 {
			d.Errors.Add("daemon", "error", st.name, "easel open", msg, nil)
		}
		d.Kick()
	}()
	return false, &openingError{"Opening the easel…"}
}

// atEasel runs one command at an open easel for the app.
func (d *Daemon) atEasel(st *studioState, args []string, input string, wait time.Duration) (string, int, error) {
	st.yieldAuto() // a hand at the easel comes before a heal
	st.easelMu.Lock()
	defer st.easelMu.Unlock()
	if err := d.easelBusy(st); err != nil {
		return "", 0, err
	}
	if ok, err := d.easelReady(st); !ok {
		return "", 0, err
	}
	st.mu.Lock()
	st.lastUse = time.Now()
	st.mu.Unlock()
	out, code := easelRun(st.dir, args, input, wait)
	st.mu.Lock()
	st.lastUse = time.Now()
	st.mu.Unlock()
	return strings.TrimRight(out, "\n"), code, nil
}

func (d *Daemon) Look(st *studioState, r LookReq) (*LookResult, error) {
	if res, err := d.keptView(st, r); res != nil || err != nil {
		return res, err
	}
	if res := d.savedCanvas(st, r); res != nil {
		return res, nil
	}
	if isView(r) && (r.Mode != "" || r.Palette) {
		if words := d.viewsComing(st); words != "" {
			return nil, &openingError{words}
		}
	}
	out, code, err := d.atEasel(st, r.args(), "", waitOther)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, &httpError{422, strings.TrimSpace(out)}
	}
	res := &LookResult{Images: []string{}}
	dest := filepath.Join(st.dir, "out/app/looks")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	lines := strings.Split(out, "\n")
	for i, ln := range lines {
		m := pngRe.FindStringSubmatch(strings.TrimSpace(ln))
		if m == nil {
			continue
		}
		src := m[1]
		if !filepath.IsAbs(src) {
			src = filepath.Join(st.dir, src)
		}
		if !exists(src) {
			continue
		}
		rel := "out/app/looks/" + newID() + ".png"
		if err := os.Rename(src, filepath.Join(st.dir, rel)); err != nil {
			return nil, err
		}
		res.Images = append(res.Images, rel)
		tail := ""
		if m[2] != "" {
			tail = fmt.Sprintf(" (%sx%s)", m[2], m[3])
			if len(res.Images) == 1 {
				res.W, res.H = atoi(m[2]), atoi(m[3])
			}
		}
		lines[i] = rel + tail
	}
	res.Said = strings.Join(lines, "\n")
	if len(res.Images) == 0 {
		return nil, &httpError{422, strings.TrimSpace(out)}
	}
	if r.whole() {
		// the studio's latest picture may be the app's: looks.jsonl remembers
		// the whole-canvas ones (a palette, a crop or a mode isn't the picture)
		b, _ := json.Marshal(appLook{T: time.Now().UnixMilli(), Path: res.Images[0], W: res.W, H: res.H})
		if f, err := os.OpenFile(filepath.Join(st.dir, "out/app/looks.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
	return res, nil
}

// savedCanvas answers a plain whole-canvas look on a closed easel with the
// picture the easel saved as it closed (live.png): the canvas exactly as the
// log left it, without the minutes a reopen takes. Any other look, an open
// easel, or a log written after the save goes to the easel.
func (d *Daemon) savedCanvas(st *studioState, r LookReq) *LookResult {
	if r.Mode != "" || r.Crop != "" || r.Size != 0 || r.Light != "" || r.Grid != nil || r.Palette || r.Scratch || r.Survey {
		return nil
	}
	st.mu.Lock()
	busy := st.opening || st.ownEasel
	st.mu.Unlock()
	live := filepath.Join(st.dir, "out/easel/painting/live.png")
	fi, err := os.Stat(live)
	lg, lerr := os.Stat(filepath.Join(st.dir, "paintings/lua/painting.lua"))
	if busy || err != nil || lerr != nil || fi.ModTime().Before(lg.ModTime()) || exists(filepath.Join(st.dir, "out/easel/painting/sock")) {
		return nil
	}
	f, err := os.Open(live)
	if err != nil {
		return nil
	}
	cfg, _, err := image.DecodeConfig(f)
	f.Close()
	if err != nil {
		return nil
	}
	rel := "out/app/looks/" + newID() + ".png"
	os.MkdirAll(filepath.Join(st.dir, "out/app/looks"), 0o755)
	b, err := os.ReadFile(live)
	if err != nil || os.WriteFile(filepath.Join(st.dir, rel), b, 0o644) != nil {
		return nil
	}
	// a 2400 px save shown as the easel's 1000 px look would: its size is said as the canvas's
	w, h := cfg.Width, cfg.Height
	return &LookResult{Said: rel + fmt.Sprintf(" (%dx%d)", w, h) + "\n(the canvas as saved when the easel last closed)", Images: []string{rel}, W: w, H: h}
}

// DoReq is POST …/do.
type DoReq struct {
	Lua        string `json:"lua"`
	Scratch    bool   `json:"scratch"`
	NewScratch bool   `json:"new_scratch"`
}

func (d *Daemon) Do(st *studioState, r DoReq) (string, error) {
	if strings.TrimSpace(r.Lua) == "" {
		return "", &httpError{400, "lua is the chunk"}
	}
	if r.NewScratch && !r.Scratch {
		return "", &httpError{400, "new_scratch goes with scratch"}
	}
	args := []string{"do", "-"}
	if r.Scratch {
		args = append(args, "--scratch")
		if r.NewScratch {
			args = append(args, "--new")
		}
	}
	out, code, err := d.atEasel(st, args, r.Lua, waitDo)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", &httpError{422, strings.TrimSpace(out)}
	}
	return out, nil
}

// CloseEasel closes an easel nobody paints at.
func (d *Daemon) CloseEasel(st *studioState) error {
	st.easelMu.Lock()
	defer st.easelMu.Unlock()
	if d.paintingNow(st) {
		return errBusy
	}
	easelRun(st.dir, []string{"close"}, "", waitOther)
	st.mu.Lock()
	st.ownEasel = false
	st.mu.Unlock()
	return nil
}

// closeIdle closes the easels the daemon opened once they have sat for
// idleClose; an easel a painter took over is the painter's.
func (d *Daemon) closeIdle() {
	d.mu.Lock()
	sts := make([]*studioState, 0, len(d.studios))
	for _, st := range d.studios {
		sts = append(sts, st)
	}
	d.mu.Unlock()
	for _, st := range sts {
		st.mu.Lock()
		own, last, opening := st.ownEasel, st.lastUse, st.opening
		st.mu.Unlock()
		if !own || opening {
			continue
		}
		if d.paintingNow(st) {
			st.mu.Lock()
			st.ownEasel = false
			st.mu.Unlock()
			continue
		}
		if time.Since(last) < idleClose {
			continue
		}
		go func(st *studioState) {
			if !st.easelMu.TryLock() {
				return
			}
			defer st.easelMu.Unlock()
			if d.paintingNow(st) {
				return
			}
			easelRun(st.dir, []string{"close"}, "", waitOther)
			st.mu.Lock()
			st.ownEasel = false
			st.mu.Unlock()
		}(st)
	}
}
