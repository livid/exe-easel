# Working in exe-easel

For any coding agent in this checkout (CLAUDE.md is a symlink to this file).
README.md says what the repository is; this says how to work in it.

| path | what |
|---|---|
| `engine/` | claude-paint, a submodule used **as published**: never commit changes inside it. Anything the engine can't do is done from outside it (the views: `server/views.go` drives the studio's own easel) |
| `harness/claude/` | the Claude Code painter: `easel-mcp.ts` imports `engine/harness/painter/easel-client.ts`; `paint <studio>` runs one session |
| `server/` | the `exe-easel` daemon on 127.0.0.1:7794, user unit `exe-easel`; `server/API.md` is its contract |
| `apps/easel/` | the Easel app; exe serves it from `apps_dirs` and relays `/v1/svc/easel/` to the daemon |
| `projects/haixing/` | six painters for the chapters of a short story (`run setup|start|status|finish`) |
| `projects/modouji/` | ten painters for the chapters of its sequel, started through the daemon (same commands) |
| `projects/shanyang/` | nine painters for the third story, 《九重葛底下的山羊》 (same commands) |
| `studios/`, `logs/` | runtime, ignored by Git |

## When something in Easel goes wrong

Read `logs/error.log` first (or `GET 127.0.0.1:7794/v1/log`): JSON lines,
newest last; `src` is `app` (what the window met) or `daemon` (its own 5xx,
failed jobs and heals, easels that didn't open), with `studio`, `where`,
`tab`, `view`. Then the studio's own logs: `out/app/*.log` (finish, clip,
views), `out/easel/painting/server.log` (the easel), `out/claude/stderr.log`
and `runs.log` (the painter).

## Working here

- Go: `export PATH=$PATH:/usr/local/go/bin`; Rust: `export PATH=$HOME/.cargo/bin:$PATH`.
- Daemon: `cd server && go test ./... && make restart` (`make install` the first
  time). Painters run in their own transient units (`exe-easel-<studio>`) and
  survive a restart; a job (finish, clip, views) does not, so restart when none
  runs (`GET /v1/studios`: no state `finishing`, `replaying` or `drawing`).
- The engine: `git -C engine fetch && git -C engine checkout <commit>`, then
  commit the submodule bump here; `cargo build --release -p easel` in `engine/`
  for the replay build the scripts use.
- App: served live from disk; reload the window. UI rules are exe's `docs/platinum.md`.
- Tests: `node tests/easel-app-test.js <out>` reads the live daemon and refuses
  every write; `node tests/easel-flash-test.js` counts blank frames while
  switching studios. The `--write` run needs `ART=http://127.0.0.1:<port>` naming a
  scratch daemon: `server/exe-easel -listen 127.0.0.1:7804 -studios <scratch> -errors <scratch>/error.log -exe ''`.
  Never run it against :7794.
- Commit on `main`; first line `Area: what changed` (Harness:, Server:, Easel:,
  Docs:); pushes only on Livid's word. No real email addresses in code, docs,
  fixtures or messages.
