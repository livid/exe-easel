package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Proc is what the scan needs of one process.
type Proc struct {
	PID  int
	Comm string
	Argv []string
	Cwd  string
}

// scanProcs reads /proc, or asks ps where there is none (macOS). ps gives
// no working directory: there a painter is known by its argv alone (the
// daemon and projects/haixing/run pass the studio's absolute path) and Stop
// finds claude by the pid file paint writes (out/claude/claude.pid).
// Processes that vanish mid-read are skipped.
func scanProcs() []Proc {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return scanPS()
	}
	var out []Proc
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := "/proc/" + e.Name()
		raw, err := os.ReadFile(dir + "/cmdline")
		if err != nil || len(raw) == 0 {
			continue
		}
		argv := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		comm, _ := os.ReadFile(dir + "/comm")
		cwd, _ := os.Readlink(dir + "/cwd")
		out = append(out, Proc{PID: pid, Comm: strings.TrimSpace(string(comm)), Argv: argv, Cwd: cwd})
	}
	return out
}

// painterOf: the studio (absolute) a `harness/claude/paint <studio>` process
// paints in, or "".
func painterOf(p Proc) string {
	for i, a := range p.Argv {
		if !strings.HasSuffix(a, "harness/claude/paint") || i+1 >= len(p.Argv) {
			continue
		}
		s := p.Argv[i+1]
		if !filepath.IsAbs(s) {
			s = filepath.Join(p.Cwd, s)
		}
		return filepath.Clean(s)
	}
	return ""
}

// isClaude: a claude CLI process (by its command name or argv[0]).
func isClaude(p Proc) bool {
	if p.Comm == "claude" {
		return true
	}
	return len(p.Argv) > 0 && filepath.Base(p.Argv[0]) == "claude"
}

// procIndex is one scan, keyed by studio path.
type procIndex struct {
	painters map[string]int // studio → paint pid
	claudes  map[string]int // studio → claude pid
	timeouts map[string]int // studio → timeout pid (paint's wrapper)
}

func indexProcs(ps []Proc) procIndex {
	ix := procIndex{painters: map[string]int{}, claudes: map[string]int{}, timeouts: map[string]int{}}
	for _, p := range ps {
		if s := painterOf(p); s != "" {
			ix.painters[s] = p.PID
		}
		if p.Cwd == "" {
			continue
		}
		cwd := filepath.Clean(p.Cwd)
		if isClaude(p) {
			ix.claudes[cwd] = p.PID
		} else if p.Comm == "timeout" {
			ix.timeouts[cwd] = p.PID
		}
	}
	return ix
}

// scanPS lists processes with ps: the pid, the command name and the whole
// command line (split on spaces: studio paths have none).
func scanPS() []Proc {
	out, err := exec.Command("ps", "-axo", "pid=,comm=,args=").Output()
	if err != nil {
		return nil
	}
	var ps []Proc
	for _, ln := range strings.Split(string(out), "\n") {
		f := strings.Fields(ln)
		if len(f) < 3 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		ps = append(ps, Proc{PID: pid, Comm: filepath.Base(f[1]), Argv: f[2:]})
	}
	return ps
}
