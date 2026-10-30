import { rm } from "node:fs/promises";
import { join } from "node:path";
import type { ConsoleMessage, Page } from "playwright";
import { type Band, shotName } from "./bands";
import { READ_FAILURE } from "./read-failure";

/** How long a band waits for the page to finish reading its fixtures, in ms. */
export const SETTLE_MS = 15_000;

/** The clock every shot is taken at, so two runs over one build render the same ages and dates. */
export const FIXED_TIME = new Date("2026-10-30T12:00:00.000Z");

/** The elements a pointer can rest on that change the picture when it does. */
const CONTROLS = [
  "a",
  "button",
  "input",
  "select",
  "textarea",
  "summary",
  "[role='button']",
  "[role='link']",
  "[tabindex]",
];

/** HOVERED_CONTROLS is each control with `:hover`, which a joined list would apply only to its last. */
const HOVERED_CONTROLS = CONTROLS.map((control) => `${control}:hover`).join(", ");

/** FREEZE_STYLE stops the motion and the text caret two runs could otherwise differ in. */
const FREEZE_STYLE = `html { caret-color: transparent !important; }
*,
*::before,
*::after {
  animation: none !important;
  transition: none !important;
  scroll-behavior: auto !important;
}`;

export type ShotResult = { ok: true; path: string } | { ok: false; reason: string };

export type ShotRequest = {
  page: Page;
  url: string;
  outDir: string;
  slug: string;
  band: Band;
};

/**
 * contextOptions is what holds two runs of one band together: its width, its pixel ratio, and a
 * fixed locale, timezone and colour scheme, with motion reduced for anything the stylesheet misses.
 */
export function contextOptions(band: Band) {
  return {
    viewport: { width: band.width, height: band.height },
    deviceScaleFactor: band.scale,
    colorScheme: "light" as const,
    reducedMotion: "reduce" as const,
    locale: "en-GB",
    timezoneId: "UTC",
  };
}

/** launchFailure words a browser that will not start, naming the two installs it needs. */
export function launchFailure(cause: unknown): Error {
  return new Error(
    `Chromium could not start: ${(cause as Error).message}\n` +
      `Install its browser in tools/shots: \`bun install\`, then ` +
      `\`bunx playwright install chromium\`.`,
  );
}

/**
 * shoot loads url at the band's width and writes its full viewport as `<slug>-<band>.png`. The shot
 * is refused, and the file the band would have written removed so an earlier run's shot cannot be
 * read as this one, while a fixture read failed, fonts are still loading, anything is aria-busy, a
 * control is under the pointer, or the page scrolls sideways.
 */
export async function shoot({
  page,
  url,
  outDir,
  slug,
  band,
}: ShotRequest): Promise<ShotResult> {
  const path = join(outDir, shotName(slug, band));
  async function refuse(reason: string): Promise<ShotResult> {
    await rm(path, { force: true });
    return { ok: false, reason };
  }

  const readFailures: string[] = [];
  page.on("console", (message: ConsoleMessage) => {
    const text = message.text();
    if (message.type() === "error" && READ_FAILURE.test(text)) {
      readFailures.push(text.match(READ_FAILURE)?.[0] ?? text);
    }
  });
  page.on("pageerror", (error: Error) => {
    readFailures.push(`the page threw: ${error.message}`);
  });

  await page.clock.setFixedTime(FIXED_TIME);
  await page.goto(url, { waitUntil: "load", timeout: SETTLE_MS });
  // The reads the app makes after its document loads. A page that never falls idle is still
  // settled by the font and busy checks below, so a timeout here is not a refusal.
  await page.waitForLoadState("networkidle", { timeout: SETTLE_MS }).catch(() => {});
  await page.addStyleTag({ content: FREEZE_STYLE });

  const fontsLoaded = await page.evaluate(async () => {
    await document.fonts.ready;
    return document.fonts.status === "loaded";
  });
  if (!fontsLoaded) {
    return refuse("the fonts did not finish loading");
  }

  const idle = await page
    .waitForFunction(() => document.querySelector('[aria-busy="true"]') === null, undefined, {
      timeout: SETTLE_MS,
    })
    .then(() => true)
    .catch(() => false);
  if (!idle) {
    return refuse(`a region still reports aria-busy after ${SETTLE_MS}ms`);
  }

  // A failed read first: it explains a short page as well as refusing the shot.
  if (readFailures.length > 0) {
    return refuse(`the app could not read its fixtures: ${readFailures.join(" | ")}`);
  }

  // The pointer is parked in the corner first: a control that still answers the pointer there is
  // what refuses the shot, rather than wherever the pointer was left.
  await page.mouse.move(0, 0);
  const hovered = await page.evaluate(
    (selector) =>
      [...document.querySelectorAll(selector)].map((element) => element.tagName.toLowerCase()),
    HOVERED_CONTROLS,
  );
  if (hovered.length > 0) {
    return refuse(`a control is under the pointer (${hovered.join(", ")})`);
  }

  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  if (overflow > 0) {
    return refuse(`the page scrolls sideways: ${overflow}px wider than the viewport`);
  }

  await page.screenshot({ path, type: "png", fullPage: false, animations: "disabled" });
  return { ok: true, path };
}
