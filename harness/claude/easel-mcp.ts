/**
 * The painter's tools for Claude Code: the same five easel tools and studio `read` as
 * engine/harness/painter/easel-tools.ts gives pi (claude-paint's own harness), served as a stdio MCP server (JSON-RPC, one
 * message a line; no SDK, no dependencies). Claude Code names them mcp__easel__paint and so on.
 *
 *   node harness/claude/easel-mcp.ts <studio>
 *
 * Each tool runs the studio's `bin/easel` through engine/harness/painter/easel-client.ts, so the replies,
 * hidden counters, renamed looks and the studio fence are pi's, word for word. A look comes
 * back as a JPEG of the PNG the easel wrote (quality 92, no chroma subsampling; the PNG stays
 * on disk for `read` and `compare`), shrunk to MAX_SIDE if it is longer (Anthropic refuses images
 * over 2000 px once a request holds more than 20). Claude Code sends every image in the context
 * again with each request, and a request over 32 MB is refused: a 1000 px look is ~400 KB as PNG
 * and ~120 KB as JPEG, and the launcher compacts at 200k tokens, so a context holds ~20 MB at most.
 */
import { spawnSync } from "node:child_process";
import { existsSync, readFileSync, statSync } from "node:fs";
import { extname, resolve } from "node:path";
import { createInterface } from "node:readline";
import { atEasel, hideCounters, logReply, lookArgs, paintReply, renameLooks, statusReply, studioPath, surveyReply, tail, toolWords } from "../../engine/harness/painter/easel-client.ts";
import { reviseJournal } from "../../engine/harness/painter/journal.ts";

const studio = resolve(process.argv[2] ?? process.cwd());
if (!existsSync(resolve(studio, "bin", "easel"))) {
	process.stderr.write(`easel-mcp: no bin/easel in ${studio}\n`);
	process.exit(2);
}

/** pi's defaults (context-images.ts): at most 20 images and 12 MB of base64 in one survey reply. */
const LIMITS = { maxImages: 20, maxImageChars: 12_000_000 };
const MAX_SIDE = 2000;

type Content = { type: "text"; text: string } | { type: "image"; data: string; mimeType: string };
type Args = Record<string, unknown>;

const text = (t: string): Content[] => [{ type: "text", text: t }];

/** A PNG's width and height from its IHDR. */
function pngSize(buf: Buffer): [number, number] | undefined {
	if (buf.length < 24 || buf.toString("ascii", 12, 16) !== "IHDR") return undefined;
	return [buf.readUInt32BE(16), buf.readUInt32BE(20)];
}

/** The image at `path` as an MCP image: a PNG as JPEG, shrunk to MAX_SIDE on its long side if it is longer. */
function image(path: string): Content {
	const ext = extname(path).toLowerCase();
	const buf = readFileSync(path);
	if (ext !== ".png") return { type: "image", data: buf.toString("base64"), mimeType: ext === ".gif" ? "image/gif" : ext === ".webp" ? "image/webp" : "image/jpeg" };
	const py =
		"import sys,io\nfrom PIL import Image\nim=Image.open(sys.argv[1]).convert('RGB');m=int(sys.argv[2])\n" +
		"im.thumbnail((m,m),Image.LANCZOS)\nb=io.BytesIO();im.save(b,'JPEG',quality=92,subsampling=0);sys.stdout.buffer.write(b.getvalue())";
	const r = spawnSync("python3", ["-I", "-c", py, path, String(MAX_SIDE)], { maxBuffer: 256 * 1024 * 1024 });
	if (r.status === 0 && r.stdout.length > 0) return { type: "image", data: r.stdout.toString("base64"), mimeType: "image/jpeg" };
	const size = pngSize(buf);
	if (size && Math.max(...size) > MAX_SIDE) throw new Error(`${path}: ${size[0]}x${size[1]} is too large to show and could not be shrunk`);
	return { type: "image", data: buf.toString("base64"), mimeType: "image/png" };
}

const on = (p: Args) => (p.scratch ? ["--scratch"] : []);
const tag = (p: Args, t: string) => (p.scratch ? `[scratch canvas]\n${t}` : t);
const str = (v: unknown) => (typeof v === "string" ? v : undefined);

const SCRATCH = {
	type: "boolean",
	description:
		"true: the scratch canvas beside the painting instead of the painting. It is set up as the painting's canvas was, with its own palette, brushes and variables, and its own log; nothing done there reaches the painting. It keeps its own clock: time spent or waited there doesn't pass for the painting.",
};

const TOOLS = [
	{
		name: "paint",
		description:
			"Run a chunk of Lua at the easel (notes/easel_guide.md). The reply is what the chunk printed, the painting's current clock, then `ok`. " +
			"A chunk that stops with an error changes nothing.",
		inputSchema: {
			type: "object",
			properties: {
				lua: { type: "string", description: "the chunk" },
				scratch: SCRATCH,
				new_scratch: { type: "boolean", description: "with scratch: put the scratch canvas aside and start a fresh one before this chunk" },
			},
			required: ["lua"],
		},
	},
	{
		name: "look",
		description:
			"Look at the canvas as it is now. Without options: the whole canvas, scaled down. " +
			'crop: "x0,y0,x1,y1" in canvas units (two opposite corners), shown at 1:1 pixels. ' +
			'mode: "value", "squint", "mirror", "relief" (a raking light on the paint\'s ridges and furrows), "gallery" (the light the picture hangs in) or several, comma-separated. ' +
			'light: "azimuth,elevation" in degrees for the relief light (default "135,25", from the upper left). size: the long side in pixels. ' +
			"grid: true, or a spacing in canvas units. " +
			"survey: true surveys the whole canvas at full detail, as several tiles (with mode, not crop or size); a partial reply lists remaining tiles to read in separate turns. " +
			"compare: the path of an earlier look, shown left of the current view; supply matching crop, mode and light options explicitly. " +
			'hold: the name of a knife (what is on it) or a pile (a fresh load), with at: "x,y" (canvas units): the loaded knife held up to the canvas there, its paint thick on the blade (crop sets the passage). Supports mode value, squint, relief or gallery and light; size, grid and mirror are unavailable with hold. It shows the paint on the knife, not how it would look laid. ' +
			"palette: true shows the palette board instead: each pile knifed out thick and smeared thin across a black stripe.",
		inputSchema: {
			type: "object",
			properties: {
				scratch: SCRATCH,
				crop: { type: "string" },
				mode: { type: "string" },
				light: { type: "string" },
				survey: { type: "boolean" },
				compare: { type: "string" },
				hold: { type: "string" },
				at: { type: "string" },
				palette: { type: "boolean" },
				size: { type: "number" },
				grid: { type: ["boolean", "number"] },
			},
		},
	},
	{
		name: "note",
		description:
			"Add an entry to your journal, notes/journal.md, stamped with the painting's time. " +
			"To revise what is already there, give the exact passage to change as `replaces`: `text` takes its place.",
		inputSchema: { type: "object", properties: { text: { type: "string" }, replaces: { type: "string" } }, required: ["text"] },
	},
	{ name: "status", description: "The canvas's setup.", inputSchema: { type: "object", properties: { scratch: SCRATCH } } },
	{
		name: "log",
		description: "The painting so far: every chunk that ran, in order (paintings/lua/painting.lua).",
		inputSchema: { type: "object", properties: { scratch: SCRATCH } },
	},
	{
		name: "read",
		description:
			"Read a file in the studio: your brief, your notes, an earlier look. Text comes back with line numbers; offset (the first line, from 1) and limit (how many lines) read part of a long file. A .png comes back as the image.",
		inputSchema: {
			type: "object",
			properties: { path: { type: "string" }, offset: { type: "number" }, limit: { type: "number" } },
			required: ["path"],
		},
	},
];

async function call(name: string, p: Args): Promise<Content[]> {
	switch (name) {
		case "paint": {
			if (typeof p.lua !== "string") throw new Error("paint: lua is the chunk");
			if (p.new_scratch && !p.scratch) throw new Error("paint: new_scratch goes with scratch: true");
			try {
				return text(tag(p, paintReply(await atEasel(studio, ["do", "-", ...on(p), ...(p.new_scratch ? ["--new"] : [])], p.lua))));
			} catch (e) {
				throw new Error(hideCounters((e as Error).message));
			}
		}
		case "look": {
			const { scratch: onScratch, ...rest } = p;
			const view = {
				crop: str(rest.crop),
				mode: str(rest.mode),
				light: str(rest.light),
				survey: rest.survey === true || undefined,
				compare: str(rest.compare) || undefined,
				hold: str(rest.hold),
				at: str(rest.at),
				palette: rest.palette === true || undefined,
				size: typeof rest.size === "number" ? rest.size : undefined,
				grid: typeof rest.grid === "boolean" || typeof rest.grid === "number" ? rest.grid : undefined,
			};
			lookArgs(view); // validate combinations before resolving compare paths
			if (view.survey && view.compare) throw new Error("look: survey and compare are two looks; ask for one");
			if (view.compare !== undefined) {
				view.compare = studioPath(studio, view.compare);
				if (view.compare === undefined) throw new Error("look: compare is the path of an earlier look in this studio");
			}
			let said: string;
			try {
				said = await atEasel(studio, ["look", ...on(p), ...lookArgs(view)], undefined);
			} catch (e) {
				throw new Error(toolWords((e as Error).message)); // the easel's messages name its command-line flags
			}
			let paths: string[];
			({ said, paths } = renameLooks(studio, said));
			if (onScratch) said = `[scratch canvas]\n${said}`;
			if (paths.length === 0) throw new Error(said);
			const images = paths.map((path) => image(resolve(studio, path))) as { type: "image"; data: string; mimeType: string }[];
			if (view.survey) return surveyReply(said, paths, images, LIMITS).content as Content[];
			return [{ type: "text", text: said }, ...images];
		}
		case "note": {
			if (typeof p.text !== "string") throw new Error("note: text is the entry");
			if (typeof p.replaces === "string") return text(reviseJournal(studio, p.replaces, p.text));
			return text(await atEasel(studio, ["note", "-"], p.text));
		}
		case "status":
			return text(tag(p, statusReply(await atEasel(studio, ["status", ...on(p)], undefined))));
		case "log": {
			const whole = p.scratch ? "paintings/lua/scratch.lua" : "paintings/lua/painting.lua";
			return text(tag(p, tail(logReply(await atEasel(studio, ["log", ...on(p)], undefined)), 50_000, whole)));
		}
		case "read": {
			const path = str(p.path);
			if (!path) throw new Error("read: path is the file to read");
			const real = studioPath(studio, path);
			if (!real) throw new Error(`${path} is outside the studio`);
			if (!existsSync(real) || !statSync(real).isFile()) throw new Error(`${path}: no such file in the studio`);
			if (/\.(png|jpe?g|gif|webp)$/i.test(real)) return [{ type: "text", text: `Read image file [${path}]` }, image(real)];
			const lines = readFileSync(real, "utf8").split("\n");
			const from = Math.max(1, Math.floor(typeof p.offset === "number" ? p.offset : 1));
			const count = Math.max(1, Math.floor(typeof p.limit === "number" ? p.limit : 2000));
			const shown = lines.slice(from - 1, from - 1 + count);
			let out = shown.map((line, i) => `${String(from + i).padStart(6)}\t${line}`).join("\n");
			if (from - 1 + count < lines.length) out += `\n\n(${lines.length - (from - 1 + count)} more lines: read on with offset ${from + count})`;
			return text(out);
		}
	}
	throw new Error(`no tool ${name}`);
}

// One tool at a time, as pi's sequential tools: a second call waits for the first.
let queue: Promise<unknown> = Promise.resolve();
const send = (msg: object) => process.stdout.write(JSON.stringify(msg) + "\n");

async function handle(msg: { id?: number | string; method?: string; params?: Args }) {
	const { id, method, params } = msg;
	if (id === undefined) return; // notifications (initialized, cancelled) need no answer
	switch (method) {
		case "initialize":
			return send({
				jsonrpc: "2.0",
				id,
				result: {
					protocolVersion: str(params?.protocolVersion) ?? "2025-06-18",
					capabilities: { tools: {} },
					serverInfo: { name: "easel", version: "1.0.0" },
				},
			});
		case "ping":
			return send({ jsonrpc: "2.0", id, result: {} });
		case "tools/list":
			return send({ jsonrpc: "2.0", id, result: { tools: TOOLS } });
		case "tools/call": {
			const name = str(params?.name) ?? "";
			const args = (params?.arguments ?? {}) as Args;
			const run = queue.then(() => call(name, args));
			queue = run.catch(() => {});
			try {
				send({ jsonrpc: "2.0", id, result: { content: await run } });
			} catch (e) {
				send({ jsonrpc: "2.0", id, result: { content: text((e as Error).message), isError: true } });
			}
			return;
		}
	}
	send({ jsonrpc: "2.0", id, error: { code: -32601, message: `no method ${method}` } });
}

createInterface({ input: process.stdin }).on("line", (line) => {
	if (!line.trim()) return;
	let msg;
	try {
		msg = JSON.parse(line);
	} catch {
		return send({ jsonrpc: "2.0", id: null, error: { code: -32700, message: "parse error" } });
	}
	handle(msg).catch((e) => process.stderr.write(`easel-mcp: ${(e as Error).stack}\n`));
});
