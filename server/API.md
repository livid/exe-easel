# exe-easel daemon API

`exe-easel` (this folder, Go, stdlib only) serves the painters' studios to the exe
desktop's Easel app (`../apps/easel`). It listens on `127.0.0.1:7794`; exe relays
`/v1/svc/easel/<path>?<query>` to it (config.json `"services": {"easel":
"http://127.0.0.1:7794"}`), so the app calls `/v1/svc/easel/v1/studios?token=…`.
The simulator is the `engine` submodule (claude-paint, unmodified; `-engine`
names another checkout): the daemon runs its `scripts/` (export, finish,
replay) from there.
JSON everywhere; errors are `{"error": "words"}` with a 4xx/5xx status.
Times are epoch milliseconds. Paths inside a studio are relative to it, with `/`.

A **studio** is a folder `<studios>/<name>` (default `/www/exe-easel/studios`) holding
`bin/easel` and `BRIEF.md`, as `scripts/export_r16_studio` makes it plus
`harness/claude/paint`'s `out/claude/` once a painter has run. Names are
`^[a-z0-9][a-z0-9-]{0,47}$`. `studios/.trash/` holds deleted ones.

## The studio object

```json
{
  "name": "haixing-3",
  "title": "Low Tide, Counting",      // from out/claude/reply.txt (see below), else ""
  "state": "painting",                // see States
  "job": null,                        // or {"kind": "finish"|"clip", "started": ms, "error": "", "auto": false}
  "error": "",                        // the last failed job's or export's words, until the next one starts
  "box": "every",                     // bin/box, else "default"
  "model": "claude-opus-5-5",         // the last run's (runs.log "start model=…"), else ""
  "effort": "high",
  "chunks": 42,                       // "--@ chunk" lines in paintings/lua/painting.lua
  "looks": 61,                        // look calls in the session (not the app's)
  "clock": "day 1, 16:40",            // the painting's clock: the last "day N, HH:MM" a paint reply printed
  "canvas": {"w": 1000, "h": 714},    // the latest look's size, else null
  "latest": "out/easel/painting/3f1e….png", // the newest whole-canvas look (see Latest), else ""
  "final": true,                      // out/final.png and out/final.jpg (its 1600 px web copy, written after it) both exist
  "clip": false,                      // out/replay.mp4 exists
  "created": 1791449536000,           // BRIEF.md's mtime
  "updated": 1791450000000,           // newest mtime of session.jsonl, painting.lua, notes/journal.md, runs.log, out/final.png, out/final.jpg, out/replay.mp4
  "started": 1791449537000,           // the last run's start (runs.log), else null
  "ended": null,                      // its end, else null
  "exit": null,                       // its exit status, else null
  "tokens": {"input": 0, "output": 0, "cache_read": 0, "cache_write": 0}, // see Tokens
  "cost_usd": 0                       // the sum of the session's result events' total_cost_usd (0 while none)
}
```

Latest: the newest look that shows the painting as it hangs — not scratch, not
palette, survey, compare, hold or crop, and mode none, `normal` or `gallery` —
the painter's (from the session) or the app's (`POST …/look` records its
whole-canvas looks in `out/app/looks.jsonl`), whichever is newer. With none, the
painter's newest look of the painting of any kind (not scratch, not palette).
`said` texts name looks relative to the studio.

Tokens: each finished run counts its `result` line's `usage`; the run in progress
counts its assistant lines, each message once (by id, its largest figures).
Claude Code writes those lines before a message ends, so a live run's output
count is low until its result arrives.

Title: a name in 《》 or 「」 in the first paragraph of `reply.txt` (the name
alone, without its brackets or an English gloss after it); otherwise the first
non-empty line, with Markdown emphasis (`**`, `*`, `_`), leading `#`s and a
leading `Title:` / `Title —` removed and surrounding quotes (“” "" ‘’ 《》 「」 '')
trimmed; "" when that leaves more than 120 characters or nothing.

### States

| state | meaning |
|---|---|
| `preparing` | `POST /v1/studios` is exporting it |
| `idle` | nothing runs; the painting may be unstarted, stopped or done |
| `painting` | a `harness/claude/paint` for this studio is running (found in /proc, whoever started it) |
| `stopping` | stop was asked; the painter has not ended yet |
| `finishing`, `replaying` | a finish or clip job runs |
| `drawing` | a views job runs (always automatic: see The views) |
| `failed` | the export failed (`error` says why); only DELETE helps |

`painting` wins over a job; jobs refuse to start while painting.

## Endpoints

- `GET /v1/studios` → `{"studios": [studio…]}`, newest `created` first.
- `GET /v1/events` → server-sent events. `event: hello` (data `{}`) first, then
  `event: studio` with a studio object whenever any field of it changes
  (the daemon polls every second), `event: removed` with `{"name"}`.
  A comment line `: ping` every 20 s.
- `POST /v1/studios` `{"name", "profile": "every", "brief": "…markdown…"}` →
  201 the studio (state `preparing`). Exports in the background with
  `R16_BRANCH=HEAD engine/scripts/export_r16_studio <profile> <dir>` (the engine as checked out), then writes
  `BRIEF.md` (the brief given, or the template `templates/free.md` when empty).
  409 if the name is taken.
- `GET /v1/profiles` → `{"profiles": [{"name": "every", "label": "Every tube"}, …]}`
  (the export script's profiles: blank, every, friedrich, sargent, inness,
  alma-tadema, tonn, hopper, giverny, impressionist).
- `GET /v1/models` → `{"models": [{"id": "claude-opus-5-5", "label": "Claude Opus 5.5"},
  {"id": "claude-fable-5-1", …}, {"id": "claude-sonnet-5-5", …}, {"id": "claude-haiku-5-5", …}],
  "efforts": ["low", "medium", "high", "xhigh", "max"], "defaults": {"model": "claude-opus-5-5", "effort": "high", "hours": 4}}`
- `GET /v1/templates/free` → `{"text": "…"}`: the default brief (`templates/free.md`).
- `GET /v1/studios/{name}` → the studio object plus `"brief"` (BRIEF.md),
  `"reply"` (reply.txt, "" if none), `"runs"` (runs.log lines),
  `"notify"` (bool: a push is due when this run ends).
- `PUT /v1/studios/{name}/brief` `{"text"}` → the studio. 409 while painting.
- `DELETE /v1/studios/{name}` → 204; the folder moves to `.trash/<name>.<unix>`.
  409 while painting, stopping or a job runs.
- `GET /v1/studios/{name}/session?after=<seq>` → `{"events": [event…], "next": seq,
  "pending": event|null}`: the parsed `out/claude/session.jsonl`, events with
  `seq > after` (all of them for `after` 0 or absent), `next` the highest seq.
  `pending` is the tool call in flight (a `tool_use` with no result yet), shaped
  as its event would be, with no result fields. See Events.
- `GET /v1/studios/{name}/files/{path…}` → a file in the studio: anything under
  `out/` (looks, final.png/.jpg, replay.mp4, replay-sheet.jpg), `BRIEF.md`,
  `notes/**`, `paintings/lua/*.lua`. Nothing else (no `bin/`). `http.ServeContent`
  (Range works for the movie), `Cache-Control: no-store` for text, `max-age=31536000,
  immutable` for `out/easel/**/*.png` (looks never change). A PNG or JPEG takes
  `?w=<px>` (16..2400): a JPEG (quality 85) at most that wide, cached under
  `out/app/thumbs/`.
- `POST /v1/studios/{name}/start` `{"model", "effort", "hours", "notify": bool}` →
  the studio (state `painting`). Runs `harness/claude/paint <studio>` with
  `PAINTER_MODEL`, `PAINTER_EFFORT`, `PAINTER_HOURS`, `RAYON_NUM_THREADS=3`, in a
  transient systemd user unit `exe-easel-<name>` (`systemd-run --user --collect`), so
  a daemon restart leaves it painting (`exe-easel-<name>-<unix>` if that name is
  taken; `setsid` when systemd-run fails). `CLAUDE` and `NODE` name the binaries. A studio
  whose session stopped resumes it (`paint` does that by itself from
  `out/claude/session_id`). 409 while painting or a job runs.
- `POST /v1/studios/{name}/stop` → the studio (state `stopping`): SIGINT to the
  studio's `claude` process; `paint` then writes the reply and closes the easel.
- `POST /v1/studios/{name}/finish` `{"varnish": true, "coats": 0.4, "cracks": true,
  "relief": false}` → the studio (state `finishing`). Runs `engine/scripts/finish_painting
  paintings/lua/painting.lua out/final.png [--coats C] [--no-varnish] [--no-cracks]
  [--relief]`, then writes `out/final.jpg` (1600 px wide, quality 88).
  `"replay": L` (5..600 seconds; 0 or absent: none) films it as well, as the same
  job's second half: the job's kind turns to `clip` (state `replaying`) and runs
  what `POST …/clip` with that length runs. 409 while painting or with no chunks.
  The options (without `replay`) are kept in `out/app/finish.json` for the heal.
- `POST /v1/studios/{name}/clip` `{"length": 75}` → the studio (state `replaying`).
  `engine/scripts/replay_clip paintings/lua/painting.lua out/replay.mp4 --length L
  --sheet out/replay-sheet.jpg --frames-dir out/app/frames`. A painting too short
  for L (the script names the most it can run) is cut again at that length from
  the same frames (`--reuse`). 409 while painting or with no chunks. A failed
  job's words land in the studio's `error`; the scripts' output is in
  `out/app/finish.log` / `clip.log`.
- `POST /v1/studios/{name}/look` `{"mode", "crop", "size", "light", "grid", "palette",
  "scratch", "survey"}` and `POST /v1/studios/{name}/do` `{"lua", "scratch", "new_scratch"}`:
  a look for the app, or a chunk by hand (logged like a painter's). **These two
  answer slowly**: exe's relay gives up on headers after 30 s and a chunk can run
  for minutes, so the daemon sends 200 and its headers at once, a space every
  15 s, then one JSON body, which is one of:
  - the answer: look `{"said", "images": [path…], "w", "h"}` (the PNG moved to
    `out/app/looks/<uuid>.png`, so the painter's look folder only holds the
    painter's), do `{"reply"}`;
  - `{"opening": "Opening the easel: replaying chunk 12 of 55…"}`: the easel was
    closed. Its save holds only the canvas, so an open replays the whole log
    (minutes for a long painting; `easel open` cut off by a clock leaves no easel),
    and the daemon runs it in the background, reading the progress from the
    easel's `server.log`. Ask again (the app does every 1.5 s) until it answers.
    An easel the daemon opened closes after 10 idle minutes;
  - `{"error", "status"}`: 409 while painting, 422 when the easel refused (a chunk
    that fails changes nothing; a look it couldn't make), 502 when the easel
    didn't open.
  A plain whole-canvas look (no mode, crop, size, light, grid, palette, scratch or
  survey) on a closed easel whose `live.png` is newer than the log answers at once
  with that picture: the canvas exactly as the easel saved it when it closed.
- `POST /v1/studios/{name}/close` → 204: closes an easel nobody paints at.

## The views

`out/app/views/` keeps the finished canvas as each of `look`'s views shows it
(`normal`, `value`, `squint`, `mirror`, `relief`, `gallery`) and the palette
board, with `log.stamp`: the stamp of the log they were drawn from. A `views`
job (state `drawing`, always automatic: heal.go) takes them with the studio's
own easel: it opens a closed easel (one replay of the log, the progress read
from its `server.log`), looks in every mode and at the palette, keeps the
looks, and closes the easel again if it opened it; cancelled while the easel
is open, it leaves it open for whoever asked. A look that is one of them whole
(no crop, size, light, grid, scratch or survey) is answered from them while the
stamp matches the log. When it doesn't and the easel is closed, the look
answers `{"opening": "Drawing the views: replaying chunk 12 of 55…"}`, waiting
on the views job already at it or starting one (another automatic job steps
aside for it), rather than open the easel for one look; an open easel answers
the look itself.

## The error log

`<repo>/logs/error.log` (`-errors` names another; Git ignores `logs/`): one JSON
object a line, newest last, rotated to `error.log.1` past 5 MB; the same error
from the same source once in 10 s. Every entry has `t`, `src` (`app` or
`daemon`), `level` (`error`, `warn`), `msg`, and `studio` and `where` when known.

- `POST /v1/log` `{"level", "msg", "where", "studio", …}` → 204: the app's own
  (a call that failed or answered 500 and up, an alert it showed, a script
  error or rejected promise, a picture or the replay that didn't load, an event
  stream that keeps failing), with `tab`, `view`, `ua` and whatever else it adds.
- The daemon writes its answers of 500 and up, failed jobs and heals, and
  easels that didn't open.
- `GET /v1/log?n=100` → `{"path", "entries": [...]}`, oldest first.

## Self-heal

Nobody has to press anything for a painting's picture or replay (`heal.go`).
Every 10 s the daemon picks one studio that wants something made, newest log
first, and starts it as an automatic job (`job.auto: true`; states `finishing`
and `replaying` as usual): one at a time across the machine, never while a
painter works, a job runs or the app's own easel is open, and only once
`paintings/lua/painting.lua` has rested for two minutes.

- `out/final.png` (+ `final.jpg`) is made when the last run in runs.log ended
  with status 0 and there is none, and made again when the log is newer, with
  the options of the last finish asked for (`out/app/finish.json`).
- `out/app/views/` (above) are drawn when missing or older than the log.
- `out/replay.mp4` is made when there is none or the log is newer, at the
  length last asked for (`out/app/clip.json`, else 75 s).
- A start, a `look` or `do`, a finish or clip asked for, or a delete cancels an
  automatic job first (its process group is stopped) instead of answering 409;
  a cancelled heal leaves no error and is picked up again later.
- A failed heal sets the studio's `error` and is recorded in
  `out/app/heal.json` with the log's stamp; it is not tried again until the
  log changes.

## Events (session)

Built from Claude Code's stream-json: each `tool_use` is paired with its
`tool_result` and becomes one event when the result arrives. `seq` counts from 1
in file order; `t` is the line's `timestamp` (or the previous event's). Every
event has `seq`, `t`, `kind`.

| kind | fields |
|---|---|
| `start` | `model`, `session` (from `system/init`; a resumed sitting starts another) |
| `say` | `text` (an assistant text block) |
| `think` | `ms` (the assistant line's `thinking_duration_ms`), `text` (usually "") |
| `paint` | `lua`, `scratch` (bool), `reply` (the result's text) or `error` |
| `look` | `args` (the tool input, as given), `said` (the result's text), `images` ([paths relative to the studio, from the result text's `….png (WxH)` lines]), `w`, `h` (of the first), or `error` |
| `note` | `text`, `replaces` (or ""), `reply` or `error` |
| `read` | `path`, `error` (if any); the file's text is not repeated |
| `tool` | `name` (`status`, `log`, or anything else), `input`, `reply` (at most 2000 characters) or `error` |
| `compact` | (a `system/compact_boundary` line) |
| `result` | `text` (the run's final message), `cost_usd`, `turns`, `error` (when `is_error`) |
| `limit` | `text` (a rate limit event whose status isn't `allowed`) |

Image payloads (base64) never leave the daemon: looks are named by path.
