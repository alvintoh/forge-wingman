import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { captureShot, FIXED_TIME, pngSize, Refused, VIEWPORTS } from "./capture";
import { findChromium, launchChromium, openPage } from "./chromium";
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

describe.skipIf(executable === null)("shots end to end", () => {
  let dist = "";
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
      copyFileSync(join(import.meta.dir, "../../web/public/fonts", font), join(dist, "fonts", font));
    }
    writeFileSync(join(dist, "index.html"), MAIN);
    writeFileSync(join(dist, "wide.html"), WIDE);
    writeFileSync(join(dist, "hover.html"), HOVER);
    const fixtures = mkdtempSync(join(tmpdir(), "shots-e2e-fixtures-"));
    cleanup.push(fixtures);
    writeFileSync(join(fixtures, "inbox.json"), '{"runs":[]}');
    server = startServer(dist, fixtures);
  });

  afterAll(() => server.stop());

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

  test(
    "two runs over the same page write byte-identical PNGs at every width",
    async () => {
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
    },
    120_000,
  );

  test(
    "refuses a page that scrolls sideways, writing no PNG",
    async () => {
      const browser = await launchChromium(executable!);
      try {
        const page = await openPage(browser.cdp, FIXED_TIME);
        const out = join(dist, "wide.png");
        await expect(
          captureShot({
            cdp: browser.cdp,
            page,
            url: `${server.url}/wide.html`,
            viewport: VIEWPORTS[0]!,
            out,
          }),
        ).rejects.toThrow(Refused);
        expect(() => readFileSync(out)).toThrow();
      } finally {
        await browser.stop();
      }
    },
    60_000,
  );

  test(
    "refuses a control hovered at every edge, writing no PNG",
    async () => {
      const browser = await launchChromium(executable!);
      try {
        const page = await openPage(browser.cdp, FIXED_TIME);
        const out = join(dist, "hover.png");
        await expect(
          captureShot({
            cdp: browser.cdp,
            page,
            url: `${server.url}/hover.html`,
            viewport: VIEWPORTS[0]!,
            out,
          }),
        ).rejects.toThrow(Refused);
        expect(() => readFileSync(out)).toThrow();
      } finally {
        await browser.stop();
      }
    },
    60_000,
  );
});
