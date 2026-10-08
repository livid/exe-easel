package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubEasel answers status/open/close/do/look/note like the easel, enough
// for the daemon: an open easel is a file, a chunk is appended to the log,
// a look copies a fixture PNG to a new look-N.png.
const stubEasel = `#!/bin/sh
cmd=$1; shift
case "$cmd" in
status) if [ -f .open ]; then echo "1 chunks · 2400px · size=600"; exit 0; fi; echo "no session: easel open painting first" >&2; exit 1 ;;
open) touch .open; echo 'easel "painting" open'; echo opens >> opens.log ;;
close) rm -f .open; echo closed ;;
do) lua=$(cat); case "$lua" in *boom*) echo 'runtime error: [string "chunk 2"]:1: boom'; echo '(the chunk failed and changed nothing)'; exit 1 ;; esac
    mkdir -p paintings/lua; printf -- '--@ chunk\n%s\n' "$lua" >> paintings/lua/painting.lua; echo "day 1, 09:30"; echo "ok · chunk 1 (0.10 s to compute)" ;;
look) mkdir -p out/easel/painting; n=$(ls out/easel/painting | wc -l); f="$PWD/out/easel/painting/look-$n.png"; cp fixture.png "$f"; echo "$f (40x30, 0.01s)" ;;
note) cat >> notes/journal.md ;;
*) echo "no command $cmd" >&2; exit 2 ;;
esac
`

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 6), uint8(y * 8), 128, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeStudio makes a stub studio <root>/<name>.
func makeStudio(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	for _, d := range []string{"bin", "notes", "paintings/lua", "out/claude"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	os.WriteFile(filepath.Join(dir, "bin/easel"), []byte(stubEasel), 0o755)
	os.WriteFile(filepath.Join(dir, "bin/box"), []byte("every\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "BRIEF.md"), []byte("# Paint\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes/journal.md"), []byte(""), 0o644)
	writePNG(t, filepath.Join(dir, "fixture.png"), 40, 30)
	return dir
}

type fakeProcs struct {
	mu sync.Mutex
	ps []Proc
}

func (f *fakeProcs) set(ps ...Proc) { f.mu.Lock(); f.ps = ps; f.mu.Unlock() }
func (f *fakeProcs) get() []Proc {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Proc(nil), f.ps...)
}

func newTestDaemon(t *testing.T) (*Daemon, *httptest.Server, string, *fakeProcs) {
	t.Helper()
	root := t.TempDir()
	studios := filepath.Join(root, "studios")
	os.MkdirAll(studios, 0o755)
	d := NewDaemon(root, studios)
	fp := &fakeProcs{}
	d.Procs = fp.get
	d.Launch = func(name, studio string, env []string) error { return nil }
	d.Push = func(title, body, tag string) error { return nil }
	srv := httptest.NewServer(d.Handler())
	t.Cleanup(srv.Close)
	return d, srv, studios, fp
}

func call(t *testing.T, method, url string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, url, rd)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	raw, _ := io.ReadAll(resp.Body)
	json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

// easelCall is how the app calls look and do: the answer's status is in
// its body once the headers have gone, and while the easel opens it says
// so and the call is made again.
func easelCall(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	for i := 0; i < 100; i++ {
		code, m := call(t, "POST", url, body)
		if code != 200 {
			return code, m
		}
		if _, opening := m["opening"]; opening {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if st, ok := m["status"].(float64); ok {
			return int(st), m
		}
		return 200, m
	}
	t.Fatalf("%s: the easel never opened", url)
	return 0, nil
}

func TestAPIStudios(t *testing.T) {
	d, srv, studios, fp := newTestDaemon(t)
	dir := makeStudio(t, studios, "one")
	makeStudio(t, studios, "Bad_Name") // not a studio name: ignored
	os.MkdirAll(filepath.Join(studios, "half"), 0o755)

	code, m := call(t, "GET", srv.URL+"/v1/studios", nil)
	list := m["studios"].([]any)
	if code != 200 || len(list) != 1 {
		t.Fatalf("%d %v", code, m)
	}
	o := list[0].(map[string]any)
	if o["name"] != "one" || o["state"] != "idle" || o["box"] != "every" || o["chunks"].(float64) != 0 {
		t.Fatalf("%v", o)
	}

	// a hand at the easel: do, a failing do, look
	code, m = call(t, "POST", srv.URL+"/v1/studios/one/do", map[string]any{"lua": "canvas{}"})
	if _, opening := m["opening"]; code != 200 || !opening {
		t.Fatalf("a closed easel opens first: %d %v", code, m)
	}
	code, m = easelCall(t, srv.URL+"/v1/studios/one/do", map[string]any{"lua": "canvas{}"})
	if code != 200 || !strings.Contains(m["reply"].(string), "ok") {
		t.Fatalf("do %d %v", code, m)
	}
	code, m = easelCall(t, srv.URL+"/v1/studios/one/do", map[string]any{"lua": "error('boom')"})
	if code != 422 || !strings.Contains(m["error"].(string), "boom") {
		t.Fatalf("failing do %d %v", code, m)
	}
	code, m = easelCall(t, srv.URL+"/v1/studios/one/look", map[string]any{"mode": "value"})
	if code != 200 {
		t.Fatalf("look %d %v", code, m)
	}
	imgs := m["images"].([]any)
	look := imgs[0].(string)
	if !strings.HasPrefix(look, "out/app/looks/") || m["w"].(float64) != 40 || !exists(filepath.Join(dir, look)) {
		t.Fatalf("look %v", m)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "out/easel/painting")); len(entries) != 0 {
		t.Fatalf("the app's look stayed in the painter's folder")
	}
	if opens, _ := os.ReadFile(filepath.Join(dir, "opens.log")); strings.Count(string(opens), "opens") != 1 {
		t.Fatalf("easel opened %q", opens)
	}
	st := d.state("one")
	if !st.ownEasel {
		t.Fatal("the daemon didn't note it opened the easel")
	}
	// a value look isn't the picture; a plain one becomes the studio's latest
	if l := d.List()[0]; l.Latest != "" {
		t.Fatalf("latest after a value look: %q", l.Latest)
	}
	_, m = easelCall(t, srv.URL+"/v1/studios/one/look", map[string]any{})
	whole := m["images"].([]any)[0].(string)
	if l := d.List()[0]; l.Latest != whole || l.Canvas == nil || l.Canvas.W != 40 {
		t.Fatalf("latest %q canvas %v, want %q", l.Latest, l.Canvas, whole)
	}

	// files: the look, a thumbnail, the log; nothing in bin
	resp, _ := http.Get(srv.URL + "/v1/studios/one/files/" + look)
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "max-age=31536000, immutable" {
		t.Fatalf("look file %d %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	resp.Body.Close()
	resp, _ = http.Get(srv.URL + "/v1/studios/one/files/" + look + "?w=20")
	img, err := jpeg.Decode(resp.Body)
	resp.Body.Close()
	if err != nil || img.Bounds().Dx() != 20 || img.Bounds().Dy() != 15 {
		t.Fatalf("thumbnail %v %v", err, img)
	}
	resp, _ = http.Get(srv.URL + "/v1/studios/one/files/paintings/lua/painting.lua")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "canvas{}") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("log %q", b)
	}
	for _, p := range []string{"bin/easel", "bin/box", "../one/BRIEF.md", "out/../bin/easel", "fixture.png"} {
		resp, _ = http.Get(srv.URL + "/v1/studios/one/files/" + p)
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}

	// the brief
	code, _ = call(t, "PUT", srv.URL+"/v1/studios/one/brief", map[string]any{"text": "# New brief\n"})
	_, m = call(t, "GET", srv.URL+"/v1/studios/one", nil)
	if code != 200 || m["brief"] != "# New brief\n" || m["chunks"].(float64) != 1 {
		t.Fatalf("detail %d %v", code, m)
	}

	// a painter at the easel: painting, and the hand is refused
	fp.set(Proc{PID: 10, Comm: "bash", Argv: []string{"bash", "/x/harness/claude/paint", dir}, Cwd: dir},
		Proc{PID: 11, Comm: "claude", Argv: []string{"claude", "-p"}, Cwd: dir})
	_, m = call(t, "GET", srv.URL+"/v1/studios", nil)
	if s := m["studios"].([]any)[0].(map[string]any); s["state"] != "painting" {
		t.Fatalf("state %v", s["state"])
	}
	for _, p := range []string{"look", "do", "finish", "clip", "start"} {
		code, m = easelCall(t, srv.URL+"/v1/studios/one/"+p, map[string]any{"lua": "x"})
		if code != 409 {
			t.Errorf("%s while painting: %d %v", p, code, m)
		}
	}
	if code, _ = call(t, "PUT", srv.URL+"/v1/studios/one/brief", map[string]any{"text": "x"}); code != 409 {
		t.Errorf("brief while painting: %d", code)
	}
	if code, _ = call(t, "DELETE", srv.URL+"/v1/studios/one", nil); code != 409 {
		t.Errorf("delete while painting: %d", code)
	}
	if st.ownEasel {
		d.closeIdle()
		if st.ownEasel {
			t.Error("a painter's easel stayed the daemon's")
		}
	}
	fp.set()

	// start: the launch is asked with the painter's env, then it counts as painting
	var gotEnv []string
	d.Launch = func(name, studio string, env []string) error { gotEnv = env; return nil }
	code, m = call(t, "POST", srv.URL+"/v1/studios/one/start", map[string]any{"model": "claude-haiku-5-5", "effort": "low", "hours": 0.5, "notify": true})
	if code != 200 || m["state"] != "painting" {
		t.Fatalf("start %d %v", code, m)
	}
	env := strings.Join(gotEnv, "\n")
	for _, want := range []string{"PAINTER_MODEL=claude-haiku-5-5", "PAINTER_EFFORT=low", "PAINTER_HOURS=0.5", "RAYON_NUM_THREADS=3", "XDG_RUNTIME_DIR=", ".cargo/bin"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q", want)
		}
	}
	if !exists(filepath.Join(dir, "out/app/notify")) {
		t.Error("no notify mark")
	}
	if code, _ = call(t, "POST", srv.URL+"/v1/studios/one/start", map[string]any{"model": "gpt"}); code != 400 {
		t.Errorf("bad model: %d", code)
	}

	// the run ends: a push, once
	pushed := make(chan string, 4)
	d.Push = func(title, body, tag string) error { pushed <- body + "|" + tag; return nil }
	fp.set(Proc{PID: 10, Comm: "bash", Argv: []string{"bash", "/x/harness/claude/paint", dir}, Cwd: dir})
	d.List()
	os.WriteFile(filepath.Join(dir, "out/claude/reply.txt"), []byte("**Title:** The Test\n\nSome words."), 0o644)
	fp.set()
	st.mu.Lock()
	st.launched = time.Time{}
	st.mu.Unlock()
	list2 := d.List()
	if list2[0].State != "idle" || list2[0].Title != "The Test" {
		t.Fatalf("after the run: %+v", list2[0])
	}
	select {
	case p := <-pushed:
		if p != "one: The Test|easel-one" {
			t.Errorf("push %q", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no push")
	}
	d.List()
	select {
	case p := <-pushed:
		t.Errorf("a second push %q", p)
	case <-time.After(100 * time.Millisecond):
	}

	// delete: into .trash
	if code, _ = call(t, "DELETE", srv.URL+"/v1/studios/one", nil); code != 204 {
		t.Fatalf("delete %d", code)
	}
	trash, _ := os.ReadDir(filepath.Join(studios, ".trash"))
	if len(trash) != 1 || !strings.HasPrefix(trash[0].Name(), "one.") || exists(dir) {
		t.Fatalf("trash %v", trash)
	}
	if code, _ = call(t, "GET", srv.URL+"/v1/studios/one", nil); code != 404 {
		t.Fatalf("after delete %d", code)
	}
}

func TestAPISessionAndEvents(t *testing.T) {
	d, srv, studios, _ := newTestDaemon(t)
	dir := makeStudio(t, studios, "two")
	raw, _ := os.ReadFile("testdata/session.jsonl")
	// the fixture names haixing-2's paths: point them at this studio
	os.WriteFile(filepath.Join(dir, "out/claude/session.jsonl"), bytes.ReplaceAll(raw, []byte(fixtureStudio), []byte(dir)), 0o644)

	code, m := call(t, "GET", srv.URL+"/v1/studios/two/session?after=0", nil)
	if code != 200 || len(m["events"].([]any)) == 0 {
		t.Fatalf("%d %v", code, m)
	}
	next := int(m["next"].(float64))
	_, m = call(t, "GET", srv.URL+"/v1/studios/two/session?after="+itoa64(int64(next-1)), nil)
	if len(m["events"].([]any)) != 1 {
		t.Fatalf("after next-1: %v", m["events"])
	}
	_, m = call(t, "GET", srv.URL+"/v1/studios/two", nil)
	if m["looks"].(float64) == 0 || m["latest"] == "" || m["clock"] == "" || m["canvas"] == nil {
		t.Fatalf("detail %v", m)
	}

	// the stream: hello, then the studio when it changes
	resp, err := http.Get(srv.URL + "/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := make(chan string, 16)
	go func() {
		buf := make([]byte, 1<<16)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				got <- string(buf[:n])
			}
			if err != nil {
				close(got)
				return
			}
		}
	}()
	var seen strings.Builder
	deadline := time.After(3 * time.Second)
	d.pollOnce()
	os.WriteFile(filepath.Join(dir, "notes/journal.md"), []byte("- day 1: hello\n"), 0o644)
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(filepath.Join(dir, "notes/journal.md"), future, future)
	d.pollOnce()
	for !strings.Contains(seen.String(), "event: studio") {
		select {
		case s := <-got:
			seen.WriteString(s)
		case <-deadline:
			t.Fatalf("stream: %q", seen.String())
		}
	}
	if !strings.HasPrefix(seen.String(), "event: hello") || !strings.Contains(seen.String(), `"name":"two"`) {
		t.Fatalf("stream %q", seen.String())
	}
}

func TestFenced(t *testing.T) {
	root := t.TempDir()
	dir := makeStudio(t, root, "s")
	os.MkdirAll(filepath.Join(dir, "out"), 0o755)
	os.WriteFile(filepath.Join(root, "secret.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "out/ok.txt"), []byte("x"), 0o644)
	os.Symlink(filepath.Join(root, "secret.txt"), filepath.Join(dir, "out/escape.txt"))
	os.Symlink(filepath.Join(dir, "bin/easel"), filepath.Join(dir, "notes/easel"))
	os.Symlink(filepath.Join(dir, "out/ok.txt"), filepath.Join(dir, "notes/fine.txt"))
	cases := map[string]bool{
		"out/ok.txt":         true,
		"notes/fine.txt":     true,
		"BRIEF.md":           true,
		"out/escape.txt":     false,
		"notes/easel":        false,
		"bin/easel":          false,
		"../secret.txt":      false,
		"out/../bin/easel":   false,
		"/etc/passwd":        false,
		"out":                false,
		"paintings/lua/x.md": false,
	}
	for rel, want := range cases {
		if _, ok := fenced(dir, rel); ok != want {
			t.Errorf("fenced(%q) = %v, want %v", rel, ok, want)
		}
	}
}

func TestProcIndex(t *testing.T) {
	ps := []Proc{
		{PID: 1, Comm: "bash", Argv: []string{"bash", "/www/exe-easel/harness/claude/paint", "/www/exe-easel/studios/a"}, Cwd: "/www/exe-easel/studios/a"},
		{PID: 2, Comm: "bash", Argv: []string{"/www/exe-easel/harness/claude/paint", "studios/b"}, Cwd: "/www/exe-easel"},
		{PID: 3, Comm: "claude", Argv: []string{"claude", "-p"}, Cwd: "/www/exe-easel/studios/a"},
		{PID: 4, Comm: "timeout", Argv: []string{"timeout", "1", "claude"}, Cwd: "/www/exe-easel/studios/a"},
		{PID: 5, Comm: "vim", Argv: []string{"vim", "harness/claude/paint"}, Cwd: "/www/exe-easel"},
	}
	ix := indexProcs(ps)
	if ix.painters["/www/exe-easel/studios/a"] != 1 || ix.painters["/www/exe-easel/studios/b"] != 2 || len(ix.painters) != 2 {
		t.Fatalf("%v", ix.painters)
	}
	if ix.claudes["/www/exe-easel/studios/a"] != 3 || ix.timeouts["/www/exe-easel/studios/a"] != 4 {
		t.Fatalf("%v %v", ix.claudes, ix.timeouts)
	}
}

func TestDownscale(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	img.Set(0, 0, color.RGBA{0, 0, 0, 255})
	out := downscale(img, 2)
	if out.Bounds().Dx() != 2 || out.Bounds().Dy() != 1 {
		t.Fatalf("%v", out.Bounds())
	}
	if r := out.RGBAAt(0, 0).R; r != 191 { // (0+255*3)/4 = 191.25
		t.Fatalf("average %d", r)
	}
}

// A studio is finished once the web copy is there too, not between the PNG
// and the JPEG the app asks for.
func TestFinalWaitsForTheWebCopy(t *testing.T) {
	d, _, studios, _ := newTestDaemon(t)
	dir := makeStudio(t, studios, "f")
	os.MkdirAll(filepath.Join(dir, "out"), 0o755)
	writePNG(t, filepath.Join(dir, "out/final.png"), 30, 20)
	if d.List()[0].Final {
		t.Fatal("finished with no final.jpg yet")
	}
	os.WriteFile(filepath.Join(dir, "out/final.jpg"), []byte("jpeg"), 0o644)
	if !d.List()[0].Final {
		t.Fatal("not finished with both")
	}
}
