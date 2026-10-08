package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readErrLog(t *testing.T, d *Daemon) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range d.Errors.Tail(1000) {
		var m map[string]any
		json.Unmarshal(raw, &m)
		out = append(out, m)
	}
	return out
}

// The app's errors land in logs/error.log under the repo, once in 10 s
// each, and GET /v1/log reads them back.
func TestErrorLogFromTheApp(t *testing.T) {
	d, srv, _, _ := newTestDaemon(t)
	entry := map[string]any{"level": "error", "msg": "net/http: timeout awaiting response headers", "studio": "haixing-6", "where": "look value", "status": 502, "tab": "canvas"}
	for i := 0; i < 3; i++ {
		if code, m := call(t, "POST", srv.URL+"/v1/log", entry); code != 204 {
			t.Fatalf("post %d %v", code, m)
		}
	}
	got := readErrLog(t, d)
	if len(got) != 1 {
		t.Fatalf("the same error three times in a row is one entry; got %d", len(got))
	}
	e := got[0]
	if e["src"] != "app" || e["studio"] != "haixing-6" || e["where"] != "look value" || e["tab"] != "canvas" || e["status"].(float64) != 502 || e["t"] == "" {
		t.Fatalf("entry %v", e)
	}
	if !strings.HasSuffix(d.Errors.Path, filepath.Join("logs", "error.log")) {
		t.Fatalf("path %s", d.Errors.Path)
	}
	code, m := call(t, "GET", srv.URL+"/v1/log?n=5", nil)
	if code != 200 || len(m["entries"].([]any)) != 1 {
		t.Fatalf("get %d %v", code, m)
	}
	if code, _ := call(t, "POST", srv.URL+"/v1/log", map[string]any{"level": "error"}); code != 400 {
		t.Fatalf("an entry with no msg: %d", code)
	}
}

// An easel that won't open is said to the app and written to the log.
func TestErrorLogFromAFailedOpen(t *testing.T) {
	d, srv, studios, _ := newTestDaemon(t)
	dir := makeStudio(t, studios, "broken")
	os.WriteFile(filepath.Join(dir, "bin/easel"), []byte("#!/bin/sh\ncase \"$1\" in status) echo 'no painting open' >&2; exit 1 ;; open) echo 'easel: fatal: the log is broken'; exit 1 ;; esac\n"), 0o755)
	code, m := call(t, "POST", srv.URL+"/v1/studios/broken/look", map[string]any{"mode": "value"})
	if _, opening := m["opening"]; code != 200 || !opening {
		t.Fatalf("first look: %d %v", code, m)
	}
	var last map[string]any
	for i := 0; i < 100; i++ {
		_, last = call(t, "POST", srv.URL+"/v1/studios/broken/look", map[string]any{"mode": "value"})
		if _, opening := last["opening"]; !opening {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if last["status"].(float64) != 502 || !strings.Contains(last["error"].(string), "the log is broken") {
		t.Fatalf("the open's failure: %v", last)
	}
	found := map[string]bool{}
	for _, e := range readErrLog(t, d) {
		if e["src"] == "daemon" && e["studio"] == "broken" {
			found[e["where"].(string)] = true
		}
	}
	if !found["easel open"] || !found["POST /v1/studios/broken/look"] {
		t.Fatalf("the log holds %v", found)
	}
}

// Canvas Now on a closed easel is the canvas the easel saved as it
// closed: no reopen, no replay of the log.
func TestCanvasNowFromTheSave(t *testing.T) {
	d, srv, studios, _ := newTestDaemon(t)
	dir := makeStudio(t, studios, "closed")
	logPath := filepath.Join(dir, "paintings/lua/painting.lua")
	os.WriteFile(logPath, []byte("--@ chunk 1\ncanvas{}\n"), 0o644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(logPath, old, old)
	os.MkdirAll(filepath.Join(dir, "out/easel/painting"), 0o755)
	writePNG(t, filepath.Join(dir, "out/easel/painting/live.png"), 60, 40)
	code, m := call(t, "POST", srv.URL+"/v1/studios/closed/look", map[string]any{})
	if code != 200 || m["w"].(float64) != 60 || m["h"].(float64) != 40 {
		t.Fatalf("canvas now: %d %v", code, m)
	}
	if exists(filepath.Join(dir, "opens.log")) {
		t.Fatal("the easel was opened for a look the save could answer")
	}
	// a mode without kept views is drawn from a replay (views.go), not an open easel
	_, m = call(t, "POST", srv.URL+"/v1/studios/closed/look", map[string]any{"mode": "value"})
	if words, _ := m["opening"].(string); !strings.Contains(words, "Drawing the views") {
		t.Fatalf("a value look: %v", m)
	}
	d.state("closed").yieldAuto()
}
