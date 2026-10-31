// Launching the machine's Chromium and opening one page on it. Nothing is
// downloaded: the runner image and a developer's Mac both carry a browser, and
// --chromium or $CHROME_PATH names one anywhere else.

import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { CDP } from "./cdp";

/** A launched browser: its CDP connection and how to shut it down. */
export type Browser = {
  cdp: CDP;
  stop: () => Promise<void>;
};

/** One open tab: its CDP session, its requests in flight and its errors. */
export type Page = {
  sessionId: string;
  // Requests the page has issued that have not finished, by request id.
  pending: Set<string>;
  // The console errors and uncaught exceptions seen while the page ran.
  errors: string[];
};

const CANDIDATES = [
  "/usr/bin/google-chrome-stable",
  "/usr/bin/google-chrome",
  "/usr/bin/chromium",
  "/usr/bin/chromium-browser",
  "/snap/bin/chromium",
  "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  "/Applications/Chromium.app/Contents/MacOS/Chromium",
  "/opt/homebrew/bin/chromium",
];

/**
 * findChromium names the browser to launch: the one given, else $CHROME_PATH,
 * else the first installed at a usual path. Null when there is none.
 */
export function findChromium(given: string | null): string | null {
  for (const candidate of [given, process.env.CHROME_PATH]) {
    if (candidate !== undefined && candidate !== null && candidate !== "" && existsSync(candidate)) {
      return candidate;
    }
  }
  return CANDIDATES.find((candidate) => existsSync(candidate)) ?? null;
}

/**
 * launchChromium starts a headless browser, waits for its debugging socket and
 * connects to it.
 */
export async function launchChromium(executable: string): Promise<Browser> {
  const userDataDir = mkdtempSync(join(tmpdir(), "wingman-shots-"));
  const child = Bun.spawn(
    [
      executable,
      "--headless",
      "--remote-debugging-port=0",
      `--user-data-dir=${userDataDir}`,
      "--no-first-run",
      "--no-default-browser-check",
      "--disable-background-networking",
      "--disable-component-update",
      "--disable-extensions",
      "--disable-sync",
      "--disable-translate",
      "--disable-features=Translate,BackForwardCache,AcceptCHFrame,MediaRouter,OptimizationHints",
      "--force-color-profile=srgb",
      "--lang=en-US",
      "--remote-allow-origins=*",
      ...(process.getuid?.() === 0 ? ["--no-sandbox", "--disable-dev-shm-usage"] : []),
      "about:blank",
    ],
    { stdout: "ignore", stderr: "pipe" },
  );

  let stderr = "";
  void (async () => {
    const reader = child.stderr.getReader();
    for (;;) {
      const { done, value } = await reader.read();
      if (done) {
        return;
      }
      stderr = (stderr + new TextDecoder().decode(value)).slice(-4_000);
    }
  })();

  const portFile = join(userDataDir, "DevToolsActivePort");
  const deadline = Date.now() + 20_000;
  let websocket = "";
  while (Date.now() < deadline) {
    if (existsSync(portFile)) {
      const [port, path] = readFileSync(portFile, "utf8").split("\n");
      if (port !== undefined && path !== undefined) {
        websocket = `ws://127.0.0.1:${port.trim()}${path.trim()}`;
        break;
      }
    }
    if (child.exitCode !== null) {
      throw new Error(`chromium exited before it was ready: ${stderr.trim()}`);
    }
    await Bun.sleep(50);
  }
  if (websocket === "") {
    child.kill();
    throw new Error(`chromium did not open its debugging port: ${stderr.trim()}`);
  }

  const cdp = await CDP.connect(websocket);
  return {
    cdp,
    stop: async () => {
      cdp.close();
      child.kill();
      await child.exited;
      rmSync(userDataDir, { recursive: true, force: true });
    },
  };
}

/**
 * openPage makes a new tab and pins the determinism settings on it before any
 * app script runs: the clock is fixed and the time zone and locale are pinned.
 * The animation-stopping style is injected per shot, at capture time.
 */
export async function openPage(cdp: CDP, fixedTime: number): Promise<Page> {
  const { targetId } = await cdp.send<{ targetId: string }>("Target.createTarget", { url: "about:blank" });
  const { sessionId } = await cdp.send<{ sessionId: string }>("Target.attachToTarget", {
    targetId,
    flatten: true,
  });
  const page: Page = { sessionId, pending: new Set(), errors: [] };

  await cdp.send("Page.enable", {}, sessionId);
  await cdp.send("Runtime.enable", {}, sessionId);
  await cdp.send("Network.enable", {}, sessionId);
  await cdp.send("Emulation.setTimezoneOverride", { timezoneId: "UTC" }, sessionId);
  await cdp.send("Emulation.setLocaleOverride", { locale: "en_US" }, sessionId);
  await cdp.send("Emulation.setEmulatedMedia", {
    features: [{ name: "prefers-reduced-motion", value: "reduce" }],
  }, sessionId);
  await cdp.send(
    "Page.addScriptToEvaluateOnNewDocument",
    { source: CLOCK_SHIM(fixedTime) },
    sessionId,
  );

  cdp.on("Network.requestWillBeSent", (params, from) => {
    if (from === sessionId && typeof params.requestId === "string") {
      page.pending.add(params.requestId);
    }
  });
  const finish = (params: Record<string, unknown>, from?: string) => {
    if (from === sessionId && typeof params.requestId === "string") {
      page.pending.delete(params.requestId);
    }
  };
  cdp.on("Network.loadingFinished", finish);
  cdp.on("Network.loadingFailed", finish);
  cdp.on("Runtime.consoleAPICalled", (params, from) => {
    if (from === sessionId && params.type === "error") {
      page.errors.push(params.args === undefined ? "console error" : JSON.stringify(params.args));
    }
  });
  cdp.on("Runtime.exceptionThrown", (params, from) => {
    if (from === sessionId) {
      page.errors.push(JSON.stringify(params.exceptionDetails ?? {}));
    }
  });

  return page;
}

// CLOCK_SHIM pins Date.now and a no-argument new Date to one instant, so the
// page's "ago" words render the same in every run. Timers are left alone.
function CLOCK_SHIM(fixedTime: number): string {
  return `(() => {
    const RealDate = Date;
    const FIXED = ${fixedTime};
    function FakeDate(...args) {
      if (!new.target) return new RealDate(FIXED).toString();
      if (args.length === 0) return new RealDate(FIXED);
      return new RealDate(...args);
    }
    FakeDate.prototype = RealDate.prototype;
    FakeDate.now = () => FIXED;
    FakeDate.parse = RealDate.parse;
    FakeDate.UTC = RealDate.UTC;
    globalThis.Date = FakeDate;
  })();`;
}
