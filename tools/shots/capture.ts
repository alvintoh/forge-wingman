// One shot: load a route in a viewport, wait until the page is settled, refuse
// to shoot a hovered control or a sideways scroll, and write the PNG.

import { writeFileSync } from "node:fs";
import type { Page } from "./chromium";

/** One review width: its CSS size, device scale factor and mobile emulation. */
export type Viewport = {
  key: "s" | "m" | "l" | "xl";
  width: number;
  height: number;
  deviceScaleFactor: number;
  mobile: boolean;
};

/**
 * The four widths the owner reviews at. `s` is the current iPhone Pro Max at its
 * Safari viewport, at three device pixels per CSS pixel.
 */
export const VIEWPORTS: readonly Viewport[] = [
  { key: "s", width: 440, height: 763, deviceScaleFactor: 3, mobile: true },
  { key: "m", width: 768, height: 1024, deviceScaleFactor: 1, mobile: false },
  { key: "l", width: 1024, height: 768, deviceScaleFactor: 1, mobile: false },
  { key: "xl", width: 1280, height: 800, deviceScaleFactor: 1, mobile: false },
];

/**
 * The instant every page's clock reads, so "ago" words are the same in both
 * runs. The fixtures' timestamps sit before it.
 */
export const FIXED_TIME = Date.UTC(2026, 2, 1, 12, 0, 0);

/** A shot the tool will not take, for a reason the caller reports rather than a bug. */
export class Refused extends Error {}

const READY_TIMEOUT_MS = 20_000;

// The elements a pointer over them would make the shot show a hover state.
const CONTROL_SELECTOR = [
  "a[href]",
  "button",
  "input",
  "select",
  "textarea",
  "summary",
  "label",
  "[role=button]",
  "[role=link]",
  "[role=radio]",
  "[role=tab]",
  "[tabindex]",
].join(",");

const STOP_ANIMATIONS = `(() => {
  const style = document.createElement("style");
  style.textContent = "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important;scroll-behavior:auto!important}";
  document.documentElement.appendChild(style);
})()`;

// READY evaluates to null when the page is settled, else a reason it is not:
// still loading, fonts pending or failed, or something marked aria-busy.
const READY = `(() => {
  if (document.readyState !== "complete") return "the document is still loading";
  if (document.fonts.status !== "loaded") return "the fonts are still loading";
  const loading = [...document.fonts].filter((face) => face.status === "loading");
  if (loading.length > 0) return "fonts still loading: " + loading.map((face) => face.family).join(", ");
  const failed = [...document.fonts].filter((face) => face.status === "error");
  if (failed.length > 0) return "fonts failed: " + failed.map((face) => face.family).join(", ");
  const busy = document.querySelector('[aria-busy="true"]');
  if (busy) return "an element is aria-busy: " + busy.tagName.toLowerCase();
  return null;
})()`;

const HOVERED_CONTROLS = `(() => {
  const selector = ${JSON.stringify(CONTROL_SELECTOR)};
  return [...document.querySelectorAll(":hover")]
    .filter((el) => el !== document.documentElement && el !== document.body && el.matches(selector))
    .map((el) => {
      const label = el.getAttribute("aria-label") ?? (el.textContent ?? "").trim().slice(0, 40);
      return el.tagName.toLowerCase() + (label === "" ? "" : ": " + label);
    });
})()`;

const SIDEWAYS_SCROLL = `(() => {
  const doc = document.documentElement;
  const width = Math.max(doc.scrollWidth, document.body === null ? 0 : document.body.scrollWidth);
  return width > doc.clientWidth ? width - doc.clientWidth : null;
})()`;

/** The CDP commands one shot makes; the real client and a test double both satisfy it. */
export type CommandChannel = {
  send<T = Record<string, unknown>>(
    method: string,
    params?: object,
    sessionId?: string,
  ): Promise<T>;
  waitFor(
    method: string,
    sessionId: string | null,
    timeoutMs: number,
  ): Promise<Record<string, unknown>>;
};

/** Everything one shot needs: the page, its URL, the viewport and the file to write. */
export type Shot = {
  cdp: CommandChannel;
  page: Page;
  url: string;
  viewport: Viewport;
  out: string;
};

/** What the page looked like before capture, and why a shot must not be taken. */
export type CaptureChecks = {
  // Controls the pointer is over at the final resting point.
  hovered: string[];
  // How many CSS pixels wider the content is than the viewport, or null.
  overflow: number | null;
  // Why the page is no longer settled at the moment of capture, or null.
  late: string | null;
};

/**
 * preCaptureRefusal answers the refusal a shot with these checks deserves, or
 * null when it may be taken.
 */
export function preCaptureRefusal(checks: CaptureChecks): Refused | null {
  if (checks.hovered.length > 0) {
    return new Refused(`a control is hovered at every edge: ${checks.hovered.join(", ")}`);
  }
  if (checks.overflow !== null) {
    return new Refused(`the page scrolls sideways: ${checks.overflow}px wider than the viewport`);
  }
  if (checks.late !== null) {
    return new Refused(`the page changed before the shot: ${checks.late}`);
  }
  return null;
}

/**
 * sizeError says why a shot's pixel size is not the viewport at its device
 * scale factor, or null when it is.
 */
export function sizeError(
  viewport: Viewport,
  size: { width: number; height: number },
): string | null {
  const wantWidth = viewport.width * viewport.deviceScaleFactor;
  const wantHeight = viewport.height * viewport.deviceScaleFactor;
  if (size.width === wantWidth && size.height === wantHeight) {
    return null;
  }
  return `the browser shot ${size.width}x${size.height} for ${viewport.key}; wanted ${wantWidth}x${wantHeight}`;
}

/** captureShot writes one shot and returns its pixel size. */
export async function captureShot(shot: Shot): Promise<{ width: number; height: number }> {
  const { cdp, page, url, viewport, out } = shot;
  const { sessionId } = page;

  await cdp.send(
    "Emulation.setDeviceMetricsOverride",
    {
      width: viewport.width,
      height: viewport.height,
      deviceScaleFactor: viewport.deviceScaleFactor,
      mobile: viewport.mobile,
    },
    sessionId,
  );
  if (viewport.mobile) {
    await cdp.send(
      "Emulation.setTouchEmulationEnabled",
      { enabled: true, maxTouchPoints: 1 },
      sessionId,
    );
  } else {
    await cdp.send("Emulation.setTouchEmulationEnabled", { enabled: false }, sessionId);
  }

  page.pending.clear();
  const loaded = cdp.waitFor("Page.loadEventFired", sessionId, READY_TIMEOUT_MS);
  await cdp.send("Page.navigate", { url }, sessionId);
  await loaded;
  await waitForQuiet(page, 150, READY_TIMEOUT_MS);

  await evaluate(cdp, sessionId, STOP_ANIMATIONS);
  await evaluate(
    cdp,
    sessionId,
    "document.activeElement instanceof HTMLElement && document.activeElement.blur()",
  );
  await evaluate(cdp, sessionId, "window.scrollTo(0, 0), document.fonts.ready.then(() => true)");

  let unsettled: string | null = null;
  await waitFor(
    async () => {
      unsettled = await evaluate<string | null>(cdp, sessionId, READY);
      return unsettled === null;
    },
    READY_TIMEOUT_MS,
    () =>
      unsettled === null ? "the page did not settle" : `the page did not settle: ${unsettled}`,
  );

  // The pointer starts at the viewport's top-left corner. Walk the corners and
  // the bottom edge until one is over no control at all; a viewport made only of
  // controls is refused rather than shot with one of them hovered.
  const points: [number, number][] = [
    [1, 1],
    [viewport.width - 2, 1],
    [viewport.width - 2, viewport.height - 2],
    [1, viewport.height - 2],
    [Math.floor(viewport.width / 2), viewport.height - 2],
  ];
  let hovered: string[] = [];
  for (const [x, y] of points) {
    await cdp.send(
      "Input.dispatchMouseEvent",
      { type: "mouseMoved", x, y, button: "none" },
      sessionId,
    );
    await settle(cdp, sessionId);
    hovered = await evaluate<string[]>(cdp, sessionId, HOVERED_CONTROLS);
    if (hovered.length === 0) {
      break;
    }
  }

  const overflow = await evaluate<number | null>(cdp, sessionId, SIDEWAYS_SCROLL);
  // Re-check at the moment of capture: nothing became busy or moved the page.
  const late = await evaluate<string | null>(cdp, sessionId, READY);
  const refusal = preCaptureRefusal({ hovered, overflow, late });
  if (refusal !== null) {
    throw refusal;
  }
  await settle(cdp, sessionId);

  const { data } = await cdp.send<{ data: string }>(
    "Page.captureScreenshot",
    { format: "png", fromSurface: true, captureBeyondViewport: false },
    sessionId,
  );
  const png = Buffer.from(data, "base64");
  const size = pngSize(png);
  const mismatch = sizeError(viewport, size);
  if (mismatch !== null) {
    throw new Error(mismatch);
  }
  writeFileSync(out, png);
  return size;
}

/** pngSize reads the width and height from a PNG's IHDR chunk. */
export function pngSize(png: Uint8Array): { width: number; height: number } {
  const signature = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a];
  if (png.length < 24 || signature.some((byte, i) => png[i] !== byte)) {
    throw new Error("the browser did not answer a PNG");
  }
  const view = new DataView(png.buffer, png.byteOffset, png.byteLength);
  return { width: view.getUint32(16), height: view.getUint32(20) };
}

async function evaluate<T>(cdp: CommandChannel, sessionId: string, expression: string): Promise<T> {
  const result = await cdp.send<{ result?: { value?: T }; exceptionDetails?: { text?: string } }>(
    "Runtime.evaluate",
    { expression, returnByValue: true, awaitPromise: true },
    sessionId,
  );
  if (result.exceptionDetails !== undefined) {
    throw new Error(`the page threw: ${result.exceptionDetails.text ?? "unknown error"}`);
  }
  return result.result?.value as T;
}

// waitFor polls until the predicate answers true; past the deadline it throws
// the caller's message.
async function waitFor(
  predicate: () => Promise<boolean>,
  timeoutMs: number,
  why: () => string,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    if (await predicate()) {
      return;
    }
    if (Date.now() >= deadline) {
      throw new Error(`${why()} within ${timeoutMs}ms`);
    }
    await Bun.sleep(25);
  }
}

// settle waits two frames, so a style or hover change has reached the screen.
async function settle(cdp: CommandChannel, sessionId: string): Promise<void> {
  await evaluate(
    cdp,
    sessionId,
    "new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)))",
  );
}

// waitForQuiet waits until the page has had no request in flight for quietMs,
// so a fetch an effect starts just after the load event is not missed.
async function waitForQuiet(page: Page, quietMs: number, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let quietSince = Date.now();
  for (;;) {
    if (page.pending.size === 0) {
      if (Date.now() - quietSince >= quietMs) {
        return;
      }
    } else {
      quietSince = Date.now();
    }
    if (Date.now() >= deadline) {
      throw new Error(`the page's requests did not finish within ${timeoutMs}ms`);
    }
    await Bun.sleep(25);
  }
}
