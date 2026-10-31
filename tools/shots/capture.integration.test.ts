import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { inflateSync } from "node:zlib";
import { captureShot, FIXED_TIME, pngSize, Refused, VIEWPORTS } from "./capture";
import type { CDP } from "./cdp";
import { findChromium, launchChromium, openPage, type Page } from "./chromium";
import { startServer, type ShotServer } from "./server";

// A synthetic build that exercises the whole capture path without the app's
// build: it fetches a fixture, clears aria-busy when the answer arrives, renders
// the clock (so the frozen clock is proven), and animates a box (so animation
// stopping is proven). Skipped when the machine has no Chromium.
const executable = findChromium(null);
const cleanup: string[] = [];

afterAll(() => {
  for (const dir of cleanup) {
    rmSync(dir, { recursive: true, force: true });
  }
});

const FONTS = `
@font-face { font-family: "IBM Plex Sans"; font-weight: 400; src: url("/fonts/IBMPlexSans-400.woff2") format("woff2"); }
@font-face { font-family: "IBM Plex Sans"; font-weight: 600; src: url("/fonts/IBMPlexSans-600.woff2") format("woff2"); }
@font-face { font-family: "IBM Plex Mono"; font-weight: 400; src: url("/fonts/IBMPlexMono-400.woff2") format("woff2"); }
@font-face { font-family: "IBM Plex Mono"; font-weight: 600; src: url("/fonts/IBMPlexMono-600.woff2") format("woff2"); }
`;

const PAGE = (body: string) => `<!doctype html>
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<style>
  ${FONTS}
  body { margin: 0; background: #f7f8fa; font-family: "IBM Plex Sans", sans-serif; }
  .mono { font-family: "IBM Plex Mono", monospace; font-weight: 600; }
  .box { width: 40px; height: 40px; background: #4f46e5; animation: spin 1s linear infinite; }
  @keyframes spin { to { transform: rotate(360deg); } }
</style>
${body}`;

const MAIN = PAGE(`<body aria-busy="true">
  <div class="box"></div>
  <p id="now"></p>
  <p class="mono">mono</p>
  <script>
    fetch("/api/inbox").then((answer) => answer.json()).then(() => {
      document.getElementById("now").textContent = new Date().toISOString();
      document.body.removeAttribute("aria-busy");
    });
  </script>
</body>`);

const WIDE = PAGE(`<body>
  <div style="width: 2000px; height: 20px; background: #111827"></div>
  <p>wide</p>
</body>`);

const HOVER = PAGE(`<body>
  <button style="position: fixed; inset: 0; width: 100vw; height: 100vh">cover</button>
</body>`);

// Busy for longer than the rest of a shot takes, so only the aria-busy gate can
// hold the shot back until the page has cleared it.
const BUSY = PAGE(`<body aria-busy="true">
  <p id="state">loading</p>
  <script>
    setTimeout(() => {
      document.getElementById("state").textContent = "ready";
      document.body.removeAttribute("aria-busy");
    }, 2500);
  </script>
</body>`);

// Reads a route no fixture answers and, like the app, never reads the failed answer.
const ABSENT = PAGE(`<body>
  <p>absent</p>
  <script>
    fetch("/api/absent").then((answer) => {
      if (!answer.ok) console.log("answered", answer.status);
    });
  </script>
</body>`);

// Taller than the phone viewport, so a scrollbar would be drawn down its right edge.
const TALL = PAGE(`<body><div style="height: 4000px"></div></body>`);

const PAGE_BACKGROUND = "247,248,250";

// rightColumn reads the colours down a PNG's right-most pixel column as "r,g,b"
// strings: it inflates the 8-bit RGB scanlines and undoes their PNG filters.
function rightColumn(png: Buffer): string[] {
  const { width, height } = pngSize(png);
  if (png[25] !== 2) {
    throw new Error("the shot is not 8-bit RGB");
  }
  const chunks: Buffer[] = [];
  for (let at = 8; at < png.length;) {
    const length = png.readUInt32BE(at);
    if (png.toString("latin1", at + 4, at + 8) === "IDAT") {
      chunks.push(png.subarray(at + 8, at + 8 + length));
    }
    at += 12 + length;
  }
  const bytes = inflateSync(Buffer.concat(chunks));
  const stride = width * 3;
  let above = new Uint8Array(stride);
  const column: string[] = [];
  for (let row = 0; row < height; row++) {
    const start = row * (stride + 1);
    const filter = bytes[start];
    const line = new Uint8Array(stride);
    for (let i = 0; i < stride; i++) {
      const raw = bytes[start + 1 + i]!;
      const left = i >= 3 ? line[i - 3]! : 0;
      const up = above[i]!;
      const upLeft = i >= 3 ? above[i - 3]! : 0;
      let predicted = 0;
      if (filter === 1) {
        predicted = left;
      } else if (filter === 2) {
        predicted = up;
      } else if (filter === 3) {
        predicted = (left + up) >> 1;
      } else if (filter === 4) {
        const estimate = left + up - upLeft;
        const toLeft = Math.abs(estimate - left);
        const toUp = Math.abs(estimate - up);
        const toUpLeft = Math.abs(estimate - upLeft);
        predicted = toLeft <= toUp && toLeft <= toUpLeft ? left : toUp <= toUpLeft ? up : upLeft;
      }
      line[i] = (raw + predicted) & 0xff;
    }
    column.push(`${line[stride - 3]},${line[stride - 2]},${line[stride - 1]}`);
    above = line;
  }
  return column;
}

describe.skipIf(executable === null)("shots end to end", () => {
  let dist = "";
  let fixtures = "";
  let server: ShotServer;

  beforeAll(() => {
    dist = mkdtempSync(join(tmpdir(), "shots-e2e-"));
    cleanup.push(dist);
    mkdirSync(join(dist, "fonts"), { recursive: true });
    for (const font of [
      "IBMPlexSans-400.woff2",
      "IBMPlexSans-600.woff2",
      "IBMPlexMono-400.woff2",
      "IBMPlexMono-600.woff2",
    ]) {
      copyFileSync(
        join(import.meta.dir, "../../web/public/fonts", font),
        join(dist, "fonts", font),
      );
    }
    writeFileSync(join(dist, "index.html"), MAIN);
    writeFileSync(join(dist, "wide.html"), WIDE);
    writeFileSync(join(dist, "hover.html"), HOVER);
    writeFileSync(join(dist, "busy.html"), BUSY);
    writeFileSync(join(dist, "absent.html"), ABSENT);
    writeFileSync(join(dist, "tall.html"), TALL);
    fixtures = mkdtempSync(join(tmpdir(), "shots-e2e-fixtures-"));
    cleanup.push(fixtures);
    writeFileSync(join(fixtures, "inbox.json"), '{"runs":[]}');
    server = startServer(dist, fixtures);
  });

  afterAll(() => server.stop());

  // withPage runs work on a page of a fresh browser and closes the browser after.
  async function withPage<T>(work: (cdp: CDP, page: Page) => Promise<T>): Promise<T> {
    const browser = await launchChromium(executable!);
    try {
      return await work(browser.cdp, await openPage(browser.cdp, FIXED_TIME));
    } finally {
      await browser.stop();
    }
  }

  async function shootInto(dir: string): Promise<void> {
    const browser = await launchChromium(executable!);
    try {
      const page = await openPage(browser.cdp, FIXED_TIME);
      for (const viewport of VIEWPORTS) {
        await captureShot({
          cdp: browser.cdp,
          page,
          url: `${server.url}/`,
          viewport,
          out: join(dir, `root-${viewport.key}.png`),
        });
      }
    } finally {
      await browser.stop();
    }
  }

  test("two runs over the same page write byte-identical PNGs at every width", async () => {
    const runs = [
      mkdtempSync(join(tmpdir(), "shots-run-a-")),
      mkdtempSync(join(tmpdir(), "shots-run-b-")),
    ];
    cleanup.push(...runs);
    for (const dir of runs) {
      await shootInto(dir);
    }
    for (const viewport of VIEWPORTS) {
      const name = `root-${viewport.key}.png`;
      const a = readFileSync(join(runs[0]!, name));
      const b = readFileSync(join(runs[1]!, name));
      expect(a.equals(b)).toBe(true);
      expect(pngSize(a)).toEqual({
        width: viewport.width * viewport.deviceScaleFactor,
        height: viewport.height * viewport.deviceScaleFactor,
      });
    }
    expect(server.fixtures.missing.size).toBe(0);
  }, 120_000);

  test("refuses a page that scrolls sideways, writing no PNG", async () => {
    const out = join(dist, "wide.png");
    await withPage(async (cdp, page) => {
      await expect(
        captureShot({ cdp, page, url: `${server.url}/wide.html`, viewport: VIEWPORTS[0]!, out }),
      ).rejects.toThrow(Refused);
    });
    expect(() => readFileSync(out)).toThrow();
  }, 60_000);

  test("refuses a control hovered at every edge, writing no PNG", async () => {
    const out = join(dist, "hover.png");
    await withPage(async (cdp, page) => {
      await expect(
        captureShot({ cdp, page, url: `${server.url}/hover.html`, viewport: VIEWPORTS[0]!, out }),
      ).rejects.toThrow(Refused);
    });
    expect(() => readFileSync(out)).toThrow();
  }, 60_000);

  test("holds a shot back until the page has cleared aria-busy", async () => {
    const stillBusy = await withPage(async (cdp, page) => {
      await captureShot({
        cdp,
        page,
        url: `${server.url}/busy.html`,
        viewport: VIEWPORTS[1]!,
        out: join(dist, "busy.png"),
      });
      const { result } = await cdp.send<{ result: { value: boolean } }>(
        "Runtime.evaluate",
        { expression: 'document.body.hasAttribute("aria-busy")', returnByValue: true },
        page.sessionId,
      );
      return result.value;
    });
    expect(stillBusy).toBe(false);
  }, 60_000);

  test("shoots a page whose read of a missing fixture is never consumed, and records the miss", async () => {
    const own = startServer(dist, fixtures);
    try {
      const size = await withPage((cdp, page) =>
        captureShot({
          cdp,
          page,
          url: `${own.url}/absent.html`,
          viewport: VIEWPORTS[1]!,
          out: join(dist, "absent.png"),
        }),
      );
      expect(size).toEqual({ width: 768, height: 1024 });
      expect([...own.fixtures.missing]).toEqual([["/api/absent", 1]]);
    } finally {
      own.stop();
    }
  }, 60_000);

  test("draws no scrollbar down the phone shot of a page taller than it", async () => {
    const out = join(dist, "tall.png");
    await withPage((cdp, page) =>
      captureShot({ cdp, page, url: `${server.url}/tall.html`, viewport: VIEWPORTS[0]!, out }),
    );
    expect(new Set(rightColumn(readFileSync(out)))).toEqual(new Set([PAGE_BACKGROUND]));
  }, 60_000);
});
