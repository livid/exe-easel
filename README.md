# exe-easel

Easel is an app for the [exe](https://github.com/livid/exe) desktop: oil
paintings made by Claude painters at a simulated easel, watched live and
kept, in a Mac OS 9 Platinum window.

The simulator is [claude-paint](https://github.com/aliceisjustplaying/claude-paint)
by Alice (MIT), the engine behind [stillwet.art](https://stillwet.art):
simulated bristles carry wet paint over primed linen, the paint levels and
dries on a clock, and layers combine by Kubelka–Munk optics. It is the
`engine` submodule here, used as published. This repository adds:

| path | what |
|---|---|
| `harness/claude/` | the painter for Claude Code: claude-paint's painter tools (`paint`, `look`, `note`, `status`, `log`, `read`) as an MCP server, and `paint <studio>`, one headless Claude Code session with those tools and nothing else |
| `server/` | `exe-easel`, a small Go daemon (stdlib only) that keeps the studios: it exports them, starts and stops painters, follows their sessions, finishes and films the paintings by itself, and serves it all to the app (`server/API.md`) |
| `apps/easel/` | the Easel app: the studios, each painter's canvas, session, journal, brief and log, a console for painting by hand, the replay |
| `projects/haixing/` | an example: six painters illustrating the six chapters of a short story |
| `tests/` | headless checks of the app |

## Setting it up

You need exe, Go, Rust (rustup), Node 24 and Claude Code signed in.

```sh
git clone --recurse-submodules https://github.com/livid/exe-easel /www/exe-easel
cd /www/exe-easel/engine && cargo build --release -p easel   # the replay build: finishing, replays
cd ../server && make install                                  # exe-easel on 127.0.0.1:7794, a user unit
```

Then tell exe about it, in `~/.exe/config.json` or with `PUT /v1/config`:

```json
"services": { "easel": "http://127.0.0.1:7794" },
"apps_dirs": [ "…", "/www/exe-easel/apps" ]
```

Easel shows on the desktop. **New Studio…** makes a studio (an easel, a box
of tubes and a brief), **Start Painter…** sets a painter to work, and the
window follows it. A finished painting is varnished, its views are drawn and
its replay filmed without anyone asking.

## Notes

- Painters run as transient systemd user units (`exe-easel-<studio>`) and
  outlive the daemon. Studios live in `studios/`; nothing there is committed.
- What goes wrong, in the app or the daemon, is written to `logs/error.log`.
- claude-paint's README asks that its benchmark data never appear in training
  corpora; this repository carries none of it (the submodule points at the
  original).
