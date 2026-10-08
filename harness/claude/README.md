# Claude Code painter harness

claude-paint's pi harness (`engine/harness/painter`) run with Claude Code instead: same system prompt,
same tools, same replies, nothing else from the machine.

| file | what it is |
|---|---|
| `easel-mcp.ts` | stdio MCP server (no dependencies; Node 24 runs the TypeScript as is) serving `paint`, `look`, `note`, `status`, `log` and a studio-fenced `read`. It calls `engine/harness/painter/easel-client.ts` and `journal.ts` unchanged, so the easel's words, the hidden chunk and look counters and the `read` fence are pi's |
| `paint` | runs one painter: `harness/claude/paint <studio> [message]` |

## What the painter gets

`paint` launches `claude -p` from the studio with:

- `--system-prompt` = `engine/harness/painter/system_prompt.md` (replaces Claude Code's own);
- `--tools ""` and `--allowedTools mcp__easel__…`: no Bash, Read, Edit, web or
  subagents, only the easel's six tools;
- `--setting-sources ""` and `--strict-mcp-config`: no user or project settings,
  hooks, CLAUDE.md, auto-memory or other MCP servers. Checked 2026-10-08 with a probe
  session that was asked to quote anything about the machine or its owner: it had
  no tools and found nothing but the account's e-mail address, which Claude Code
  attaches to every session of a logged-in account;
- `--autocompact 200k`, `--model` (`PAINTER_MODEL`, default `claude-opus-5-5`),
  `--effort` (`PAINTER_EFFORT`, default `high`, as the pi Opus lane's `--thinking high`).

It writes `out/claude/session.jsonl` (stream-json), `stderr.log`, `runs.log`,
`session_id` (a second run of `paint` on the same studio resumes the session) and
`reply.txt`, then closes the easel so `scripts/finish_painting` finishes from the save.
`PAINTER_HOURS` (default 4) stops a session at a wall-clock limit; the log is the
painting so far either way.

## Images

pi resizes and prunes the images it resends; Claude Code resends every image in the
context with each request, and the API refuses a request over 32 MB. So `look` sends
a JPEG of the easel's PNG (quality 92, no chroma subsampling, at most 2000 px on a
side — the API's limit once a request holds over 20 images), and the PNG stays on disk
for `read` and `compare`. A 1000 px look is about 400 KB as PNG and 30–120 KB as JPEG;
with compaction at 200k tokens a context holds well under 32 MB of images.

Claude Code adds a line to each image result naming where it keeps a copy (under
`~/.claude/projects/`); `read` refuses that path, as it is outside the studio.

## Verification (2026-10-08)

- `easel-mcp.ts` driven over JSON-RPC on an exported studio: `paint`, `look` (whole,
  survey of four tiles, `size` 2400 shrunk to 2000), a Lua error that changes nothing,
  `read` outside the studio refused, `read` with offset/limit, `note`, `status`.
- A Haiku 5.5 pilot through `paint` (`PAINTER_MODEL=haiku`): read the brief and the
  guide, set up a canvas, painted, looked (the image reached the model, which described
  it), noted; 12 turns. `scripts/finish_painting` finished it from the save.
