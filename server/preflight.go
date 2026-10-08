package main

// The preflight: what Easel needs beside the daemon, checked when it starts,
// so a missing piece shows at once rather than after a long painting (the
// replay is made by itself when a painter is done, and without FFmpeg it
// failed only then). Each problem is written to the error log and kept for
// GET /v1/health, which the app shows on its status line when it opens.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Preflight checks the tools and records what is missing in d.Problems.
func (d *Daemon) Preflight() {
	var problems []string
	env := d.env()
	if lookIn(env, "ffmpeg") == "" || lookIn(env, "ffprobe") == "" {
		problems = append(problems, "FFmpeg isn't installed (ffmpeg and ffprobe, with libx264): replays can't be made. macOS: brew install ffmpeg; Debian/Ubuntu: apt install ffmpeg")
	} else if out, err := exec.Command(lookIn(env, "ffmpeg"), "-hide_banner", "-encoders").CombinedOutput(); err != nil {
		// a Homebrew FFmpeg whose libraries were upgraded under it fails to load
		first := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
		problems = append(problems, "FFmpeg is installed but doesn't run ("+first+"): replays can't be made. macOS: brew reinstall ffmpeg")
	} else if !strings.Contains(string(out), "libx264") {
		problems = append(problems, "FFmpeg has no libx264 encoder: replays can't be made. Install an FFmpeg built with libx264 (Homebrew's and Debian's are)")
	}
	if lookIn(env, "uv") == "" {
		problems = append(problems, "uv isn't installed: the engine's export runs its scripts with it, so New Studio can't work. macOS: brew install uv; elsewhere: curl -LsSf https://astral.sh/uv/install.sh | sh")
	}
	if !exists(filepath.Join(d.Engine, "target/release/easel")) {
		problems = append(problems, fmt.Sprintf("the engine's replay easel isn't built: finishing and replays can't run. Run: cd %s && cargo build --release -p easel", d.Engine))
	}
	if !exists(filepath.Join(d.Engine, "scripts/export_r16_studio")) {
		problems = append(problems, "the engine submodule is empty: New Studio can't work. Run: git submodule update --init")
	}
	if d.Node == "" || !exists(d.Node) {
		problems = append(problems, "node isn't found: painters can't use the easel. Install Node 23.6 or newer and pass -node")
	}
	if d.Claude == "" || !exists(d.Claude) {
		problems = append(problems, "claude isn't found: painters can't start. Install Claude Code, sign in, and pass -claude")
	}
	d.mu.Lock()
	d.Problems = problems
	d.mu.Unlock()
	for _, p := range problems {
		d.Errors.Add("daemon", "warn", "", "preflight", p, nil)
	}
}

// lookIn finds a program on the PATH the daemon gives its children (env),
// which has Homebrew's and cargo's bins that a service manager's own PATH
// may lack; "" when it isn't there.
func lookIn(env []string, name string) string {
	for _, kv := range env {
		if !strings.HasPrefix(kv, "PATH=") {
			continue
		}
		for _, dir := range filepath.SplitList(kv[len("PATH="):]) {
			p := filepath.Join(dir, name)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
				return p
			}
		}
	}
	return ""
}
