// The Easel app in headless Chromium, against exe (for /apps/easel/ and
// /platinum/popup.css) with every /v1/svc/art/* call routed to the art
// daemon named by ART (default the live one, 127.0.0.1:7794).
//
//   node tests/easel-app-test.js <outdir> [--write]
//
// Without --write the test only reads: it lists the studios, opens each
// tab of the first one and screenshots the window at DPR 1, 1.5 and 2 and
// a 390px phone. Every PUT, POST and DELETE is refused, so the live
// painters are never touched (and /v1/workspace is never written).
//
// With --write (ART must then name a scratch daemon, never :7794) it
// creates a studio, paints a chunk at the Easel tab, looks, finishes,
// makes a replay, saves to a stubbed Workspace and moves the studio to the
// Trash, checking the window after each step.
//
// Node: ~/.nvm/versions/node/v24.15.0/bin/node. Playwright: ~/tools/playwright.
const { chromium } = require(process.env.HOME + "/tools/playwright/node_modules/playwright");
const fs = require("fs");
const http = require("http");
const path = require("path");

const OUT = process.argv[2] || "/tmp/easel-test";
const WRITE = process.argv.includes("--write");
const EXE = process.env.EXE || "http://127.0.0.1:7777";
const ART = process.env.ART || "http://127.0.0.1:7794";
if (WRITE && /:7794\b/.test(ART)) { console.error("--write needs ART set to a scratch daemon, not the live :7794"); process.exit(2); }
fs.mkdirSync(OUT, { recursive: true });

let failures = 0;
const check = (ok, what) => { console.log((ok ? "ok   " : "FAIL ") + what); if (!ok) failures++; };

// the art daemon, answered through Node's http (fetch would drop a Host
// header; an SSE stream must pass through as it comes)
function relay(route) {
  const req = route.request();
  const u = new URL(req.url());
  const p = u.pathname.replace(/^\/v1\/svc\/art/, "") || "/";
  u.searchParams.delete("token");
  const method = req.method();
  if (!WRITE && method !== "GET" && method !== "HEAD") {
    return route.fulfill({ status: 403, contentType: "application/json", body: JSON.stringify({ error: "the read-only test refuses " + method }) });
  }
  // the live daemon is reached the real way, through exe's relay (the
  // event stream included); a scratch one through this process
  if (!WRITE) return route.continue();
  if (p === "/v1/events") {
    // EventSource through a route cannot stream: answer the hello and end,
    // the page reconnects every 5 s and reloads the list on each hello
    return route.fulfill({ status: 200, contentType: "text/event-stream", body: "event: hello\ndata: {}\n\n" });
  }
  return new Promise(done => {
    const target = new URL(ART);
    const r = http.request({ host: target.hostname, port: target.port, method, path: p + (u.search || ""), headers: Object.fromEntries(Object.entries({ "content-type": req.headers()["content-type"] || "application/json", range: req.headers()["range"] }).filter(([, v]) => v)) }, res => {
      const chunks = [];
      res.on("data", c => chunks.push(c));
      res.on("end", () => { route.fulfill({ status: res.statusCode, headers: Object.fromEntries(Object.entries(res.headers).filter(([k]) => !["transfer-encoding", "connection"].includes(k))), body: Buffer.concat(chunks) }).then(done, done); });
    });
    r.on("error", e => route.fulfill({ status: 502, body: String(e) }).then(done, done));
    const body = req.postDataBuffer();
    if (body) r.write(body);
    r.end();
  });
}

async function open(browser, dpr, mobile, w = 980, h = 640) {
  const ctx = await browser.newContext({ viewport: { width: w, height: h }, deviceScaleFactor: dpr });
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", e => errors.push(e.message));
  page.on("console", m => { if (m.type() === "error" && !/Failed to load resource|EventSource/.test(m.text())) errors.push(m.text()); });
  await page.route("**/v1/svc/art/**", relay);
  const saved = [];
  await page.route("**/v1/workspace/**", route => {
    const r = route.request();
    if (r.method() === "GET" || r.method() === "HEAD") return route.continue();
    saved.push({ method: r.method(), url: r.url(), bytes: (r.postDataBuffer() || Buffer.alloc(0)).length });
    return route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
  });
  await page.goto(EXE + "/apps/easel/" + (mobile ? "?mobile=1" : ""), { waitUntil: "domcontentloaded" });
  return { ctx, page, errors, saved };
}
const shot = (page, name) => page.screenshot({ path: path.join(OUT, name + ".png") });
const settle = page => page.waitForTimeout(700);

async function readOnly(browser) {
  for (const dpr of [1, 1.5, 2]) {
    const { ctx, page, errors } = await open(browser, dpr, false);
    await page.waitForSelector("#st-list .st", { timeout: 15000 });
    await page.evaluate(() => localStorage.clear());
    const rows = await page.$$eval("#st-list .st", r => r.map(x => x.dataset.name));
    check(rows.length > 0, `dpr ${dpr}: ${rows.length} studios listed`);
    for (const tab of ["canvas", "session", "journal", "brief", "code", "easel", "replay"]) {
      await page.click(`#tabs .tab[data-tab="${tab}"]`);
      await page.waitForTimeout(tab === "session" ? 2500 : 1200);
      await shot(page, `dpr${dpr}-${tab}`);
    }
    // no layout outside the window, the status line clipped to 15px
    const m = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, iw: innerWidth, st: document.querySelector("#status").getBoundingClientRect().height }));
    check(m.sw <= m.iw, `dpr ${dpr}: no sideways overflow (${m.sw} <= ${m.iw})`);
    check(m.st === 15 || Math.abs(m.st - 15) < 0.01, `dpr ${dpr}: status bar 15px (${m.st})`);
    if (dpr === 1) {
      // the session's look thumbnails keep their shape before they load
      await page.click('#tabs .tab[data-tab="session"]');
      await page.waitForTimeout(2500);
      const looks = await page.$$eval("#se-list .ev-look img", imgs => imgs.slice(0, 5).map(i => [i.getAttribute("width"), i.getAttribute("height")]));
      check(looks.length === 0 || looks.every(([w, h]) => +w > 0 && +h > 0), `session looks carry width and height (${JSON.stringify(looks)})`);
      const evs = await page.$$eval("#se-list .ev", e => e.length);
      check(evs > 0, `session shows ${evs} events`);
      // a paint row unfolds its code
      const paint = await page.$("#se-list .ev-paint .hd");
      if (paint) { await paint.click(); check(await page.$eval("#se-list .ev-paint", e => e.classList.contains("open") && getComputedStyle(e.querySelector(".code")).display === "block"), "a paint row unfolds its Lua"); }
      // the brief: read-only test refuses the PUT (a painter is at work or the save is refused)
      await page.click('#tabs .tab[data-tab="brief"]');
      await page.waitForTimeout(800);
      check((await page.$eval("#br", t => t.value.length)) > 0, "the brief shows");
      // the context menu
      await page.click("#st-list .st", { button: "right" });
      await page.waitForTimeout(400);
      const items = await page.$$eval("#ctx .dd-item", d => d.map(x => x.textContent + (x.classList.contains("dis") ? " (dis)" : "")));
      check(items.includes("Start Painter…") || items.includes("Start Painter… (dis)"), "context menu: " + items.join(", "));
      await shot(page, "dpr1-menu");
      await page.keyboard.press("Escape");
      // the canvas views pop-up
      await page.click('#tabs .tab[data-tab="canvas"]');
      const opts = await page.$$eval("#view option", o => o.map(x => x.value + (x.disabled ? "(dis)" : "")));
      console.log("     view options: " + opts.join(" "));
    }
    check(errors.length === 0, `dpr ${dpr}: no page errors ${errors.length ? JSON.stringify(errors) : ""}`);
    await ctx.close();
  }
  // a phone: the list, then a studio
  const { ctx, page, errors } = await open(browser, 3, true, 390, 760);
  await page.waitForSelector("#st-list .st", { timeout: 15000 });
  await settle(page);
  await shot(page, "phone-list");
  await page.click("#st-list .st");
  await page.waitForTimeout(1500);
  await shot(page, "phone-canvas");
  await page.click('#tabs .tab[data-tab="session"]');
  await page.waitForTimeout(2500);
  await shot(page, "phone-session");
  const m = await page.evaluate(() => ({ sw: document.documentElement.scrollWidth, iw: innerWidth, grow: getComputedStyle(document.querySelector("#grow")).display }));
  check(m.sw <= m.iw, `phone: no sideways overflow (${m.sw})`);
  check(m.grow === "none", "phone: no grow box");
  check(errors.length === 0, `phone: no page errors ${errors.length ? JSON.stringify(errors) : ""}`);
  await ctx.close();
}

async function writes(browser) {
  const { ctx, page, errors, saved } = await open(browser, 1, false);
  await page.waitForSelector("#b-new:not([disabled])");
  await page.evaluate(() => localStorage.clear());
  const name = "uitest-" + Date.now().toString(36);
  await page.click("#b-new");
  await page.waitForSelector("#d-name");
  await shot(page, "w-new-sheet");
  await page.fill("#d-name", name);
  await page.selectOption("#d-box", "blank");
  await page.uncheck("#d-go");
  await page.click("#d-ok");
  await page.waitForFunction(n => [...document.querySelectorAll("#st-list .st")].some(r => r.dataset.name === n), name, { timeout: 10000 });
  // the export takes a while; the list reports it
  await page.waitForFunction(n => { const r = document.querySelector(`#st-list .st[data-name="${n}"] .sub`); return r && !/Setting up/.test(r.textContent); }, name, { timeout: 300000, polling: 2000 });
  check(true, "studio created: " + name);
  await page.click(`#st-list .st[data-name="${name}"]`);
  await page.click('#tabs .tab[data-tab="easel"]');
  await page.waitForSelector("#ez-in:not([disabled])");
  await page.fill("#ez-in", 'canvas{size=400, aspect=1.5, linen=20, seed=3, ground={{pile={{"lead white",4},{"yellow ochre",0.3}}, um=60, apply="knife"}}}\n' +
    'local p = pile{{"lead white",3},{"cobalt blue",1}, medium=0.2}\nlocal b = brush{kind="flat", width=40}\n' +
    // enough hand time for a replay of a few seconds: a short painting has none to film
    'for i = 1, 90 do b:load(p); b:stroke({{40 + (i * 37) % 900, 60 + (i * 53) % 560}, {120 + (i * 37) % 900, 90 + (i * 29) % 560}}, {pressure=0.7}) end\nprint(wait(30))');
  await page.click("#ez-run");
  await page.waitForSelector("#ez-out .ez img, #ez-out pre.err", { timeout: 120000 });
  const err = await page.$eval("#ez-out", o => (o.querySelector("pre.err") || {}).textContent || "");
  check(!err, "a chunk ran and the console looked" + (err ? ": " + err : ""));
  if (err) throw new Error(err);
  await shot(page, "w-console");
  await page.click('#tabs .tab[data-tab="canvas"]');
  await page.selectOption("#view", "now");
  await page.waitForFunction(() => !document.querySelector("#cv-img").hidden && document.querySelector("#cv-img").complete, null, { timeout: 60000 });
  await page.selectOption("#view", "relief");
  await page.waitForFunction(() => /Relief/.test(document.querySelector("#cv-dims").textContent), null, { timeout: 60000 });
  await settle(page);
  await shot(page, "w-relief");
  // finish
  await page.click("#b-finish");
  await page.waitForSelector("#d-varnish");
  await page.click("#d-ok");
  await page.waitForFunction(n => { const r = document.querySelector(`#st-list .st[data-name="${n}"] .sub`); return r && /Finished/.test(r.textContent); }, name, { timeout: 300000, polling: 2000 });
  await page.waitForTimeout(1500);
  check(/Finished/.test(await page.$eval("#cv-dims", e => e.textContent)), "the finished picture shows");
  await shot(page, "w-finished");
  // replay
  await page.click("#b-clip");
  await page.waitForSelector("#d-len");
  await page.fill("#d-len", "12");
  await page.click("#d-ok");
  await page.click('#tabs .tab[data-tab="replay"]');
  await page.waitForFunction(() => !document.querySelector("#rp-save").disabled, null, { timeout: 600000, polling: 2000 });
  await page.waitForTimeout(2000);
  await shot(page, "w-replay");
  // Playwright's Chromium has no H.264 (canPlayType says ""): there the
  // movie is checked as served, with byte ranges for seeking; a browser
  // with the codec plays it
  const h264 = await page.evaluate(() => document.createElement("video").canPlayType('video/mp4; codecs="avc1.640028"'));
  if (h264) {
    await page.click(".mv-play");
    await page.waitForTimeout(1500);
    check(await page.$eval("#rp-box video", v => v.currentTime > 0), "the replay plays");
  } else {
    const st = await page.evaluate(async () => (await fetch(document.querySelector("#rp-box video").src, { headers: { Range: "bytes=0-99" } })).status);
    check(st === 206, `the replay is served with ranges (${st}); this Chromium has no H.264 to play it`);
  }
  // save to the (stubbed) Workspace
  await page.click("#b-save");
  await page.waitForTimeout(1500);
  await page.click("#rp-save");
  await page.waitForTimeout(2500);
  check(saved.length === 2 && saved.every(s => s.bytes > 1000), "saved to the Workspace: " + JSON.stringify(saved.map(s => [decodeURIComponent(s.url.split("/v1/workspace/")[1]), s.bytes])));
  // the trash
  await page.click(`#st-list .st[data-name="${name}"]`, { button: "right" });
  await page.click("#ctx .dd-item:has-text('Move to Trash')");
  await page.waitForSelector("#veil.alert");
  await shot(page, "w-trash-confirm");
  await page.click("#a-ok");
  await page.waitForFunction(n => !document.querySelector(`#st-list .st[data-name="${n}"]`), name, { timeout: 10000 });
  check(true, "moved to the Trash");
  check(errors.length === 0, `writes: no page errors ${errors.length ? JSON.stringify(errors) : ""}`);
  await ctx.close();
}

(async () => {
  // the scrollbars are part of the look: headless hides them unless told
  const browser = await chromium.launch({ ignoreDefaultArgs: ["--hide-scrollbars"], args: ["--disable-gpu", "--use-angle=swiftshader", "--enable-unsafe-swiftshader"] });
  try {
    if (WRITE) await writes(browser); else await readOnly(browser);
  } finally { await browser.close(); }
  console.log(failures ? `${failures} failed` : "all passed");
  process.exit(failures ? 1 : 0);
})();
