// exe-easel serves the painters' studios (folders the engine's
// scripts/export_r16_studio makes, painted by harness/claude/paint) to the exe desktop's Easel app: the studio
// list and its live changes, each painter's session, the files a studio
// makes, and the actions the app takes (start, stop, finish, replay, a hand
// at the easel). API.md is the contract.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	home, _ := os.UserHomeDir()
	defClaude := filepath.Join(home, ".local/bin/claude")
	if p, err := exec.LookPath("claude"); err == nil {
		defClaude = p
	}
	listen := flag.String("listen", "127.0.0.1:7794", "address to serve on")
	repo := flag.String("repo", "/www/exe-easel", "the exe-easel checkout")
	engine := flag.String("engine", "", "the claude-paint checkout: the simulator, its scripts and the painter's notes (default <repo>/engine, the submodule)")
	studios := flag.String("studios", "", "the studios folder (default <repo>/studios)")
	claude := flag.String("claude", defClaude, "the claude CLI")
	node := flag.String("node", filepath.Join(home, ".nvm/versions/node/v24.15.0/bin/node"), "node 24, for the easel MCP server")
	exe := flag.String("exe", "http://127.0.0.1:7777", "exe's API, for POST /v1/push (empty: no pushes)")
	exeConfig := flag.String("exe-config", filepath.Join(home, ".exe/config.json"), "exe's config, read for its api_token")
	errors := flag.String("errors", "", "the error log (default <repo>/logs/error.log); a scratch daemon gives its own")
	flag.Parse()
	if *studios == "" {
		*studios = filepath.Join(*repo, "studios")
	}
	if *engine == "" {
		*engine = filepath.Join(*repo, "engine")
	}
	d := NewDaemon(*repo, *studios)
	d.Engine = *engine
	d.Claude, d.Node, d.Exe = *claude, *node, *exe
	if *errors != "" {
		d.Errors = NewErrLog(*errors)
	}
	if b, err := os.ReadFile(*exeConfig); err == nil {
		var c struct {
			APIToken string `json:"api_token"`
		}
		if json.Unmarshal(b, &c) == nil {
			d.ExeToken = c.APIToken
		}
	}
	d.Adopt()
	go d.Poll(make(chan struct{}))
	srv := &http.Server{Addr: *listen, Handler: d.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("exe-easel: serving %s on %s", d.Studios, *listen)
	log.Fatal(srv.ListenAndServe())
}
