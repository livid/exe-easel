# exe-art

A local fork of github.com/aliceisjustplaying/claude-paint (MIT; the oil
paint simulator behind stillwet.art) that runs its painters with Claude
Code and shows them on the exe desktop. Upstream's own README and notes/
cover the simulator; this file covers what the fork adds.

| path | what |
|---|---|
| `harness/claude/` | the painter for Claude Code: `easel-mcp.ts` (the easel's tools as an MCP server), `paint <studio>` (one headless session); its README says what the painter gets |
| `server/` | the `exe-art` daemon (Go, stdlib) on 127.0.0.1:7794, user unit `exe-art`; `server/API.md` is its contract |
| `apps/easel/` | the Easel app; exe serves it from `apps_dirs` and relays `/v1/svc/art/` to the daemon |
| `projects/haixing/` | six painters for the chapters of a short story (`run setup|start|status|finish`) |
| `studios/` | the studios (ignored by Git) |
| `logs/error.log` | **what went wrong**, from the app and the daemon (ignored by Git) |

## When something in Easel goes wrong

Read `logs/error.log` first (or `GET 127.0.0.1:7794/v1/log`). It is JSON
lines, newest last. Each line has `src` (`app`: what the window met;
`daemon`: its own 5xx, failed jobs, heals and easel opens), plus `studio`,
`where`, `tab` and `view`. Then the studio's own logs: `out/app/*.log`
(finish, clip), `out/easel/painting/server.log` (the easel), and
`out/claude/stderr.log` and `runs.log` (the painter).

## Working here

- Go: `export PATH=$PATH:/usr/local/go/bin`; Rust: `export PATH=$HOME/.cargo/bin:$PATH`.
- Daemon: `cd server && go test ./... && make restart` (`make install` the first time).
  Painters run in their own transient units (`exe-art-<studio>`) and survive the
  restart. A job (finish, clip) does not, so restart when none runs
  (`GET /v1/studios`: no state `finishing` or `replaying`).
- App: served live from disk; reload the window. UI rules are exe's
  `docs/platinum.md`.
- Tests: `node tests/easel-app-test.js <out>` reads the live daemon and refuses
  every write. The `--write` run needs `ART=http://127.0.0.1:<port>` pointing at a
  scratch daemon: `server/exe-art -listen 127.0.0.1:7804 -studios <scratch> -errors <scratch>/error.log -exe ''`.
  Never run it against :7794.
- Commit on `main`; first line `Area: what changed` (Harness:, Server:, Easel:);
  pushes and a GitHub fork only on Livid's word. No real email addresses.
