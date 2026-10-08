// node tests/easel-flash-test.js — reads the live daemon, refuses writes.
// OLD=<an index.html> serves that page instead (to see a flash the test catches).
// Click through the studios in the live Easel (writes refused) and record,
// every animation frame, whether the canvas box shows a picture that has
// painted: a frame with none while the chosen studio has one is a flash.
const { chromium } = require(process.env.HOME + "/tools/playwright/node_modules/playwright");
(async () => {
  const b = await chromium.launch({ args: ["--disable-gpu"] });
  const p = await b.newPage({ viewport: { width: 980, height: 640 } });
  await p.route("**/v1/svc/easel/**", r => r.request().method() === "GET" || r.request().url().endsWith("/log") ? r.continue() : r.fulfill({ status: 403, body: "{}" }));
  if (process.env.OLD) await p.route("http://127.0.0.1:7777/apps/easel/", r => r.fulfill({ body: require("fs").readFileSync(process.env.OLD, "utf8"), contentType: "text/html; charset=utf-8" }));
  await p.goto("http://127.0.0.1:7777/apps/easel/");
  await p.waitForSelector("#st-list .st");
  await p.waitForTimeout(2500); // the prefetch runs while idle
  await p.evaluate(() => {
    window.blank = []; window.frames = 0;
    const tick = () => {
      const img = document.querySelector("#cv-img");
      const ok = img && !img.hidden && img.complete && img.naturalWidth > 0;
      window.frames++;
      if (!ok) window.blank.push(performance.now());
      requestAnimationFrame(tick);
    };
    requestAnimationFrame(tick);
  });
  const names = await p.$$eval("#st-list .st", r => r.map(x => x.dataset.name));
  for (let round = 0; round < 2; round++) for (const n of names) {
    await p.click(`#st-list .st[data-name="${n}"]`);
    await p.waitForTimeout(350);
  }
  const r = await p.evaluate(() => ({ frames: window.frames, blank: window.blank.length }));
  if (r.blank) process.exitCode = 1;
  console.log(`studios ${names.length}, switches ${names.length * 2}, frames ${r.frames}, frames with no picture: ${r.blank}`);
  await b.close();
})();
