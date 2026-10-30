import type { Browser } from "playwright";
import { chromium } from "playwright";
import type { Band } from "./bands";
import { contextOptions, launchFailure, type ShotResult, shoot } from "./shoot";

export type CaptureOptions = {
  browser: Browser;
  url: string;
  outDir: string;
  slug: string;
  band: Band;
};

export type CaptureResult = ShotResult;

/**
 * launchChromium starts the browser every shot is taken in. Playwright arrives with this tool's own
 * dependencies, so a run before `bun install` fails here rather than mid-capture.
 */
export async function launchChromium(): Promise<Browser> {
  try {
    return await chromium.launch({ headless: true });
  } catch (err) {
    throw launchFailure(err);
  }
}

/** capture takes one band's shot in a fresh context, so no earlier band's state or scale carries. */
export async function capture({
  browser,
  url,
  outDir,
  slug,
  band,
}: CaptureOptions): Promise<CaptureResult> {
  const context = await browser.newContext(contextOptions(band));
  try {
    return await shoot({ page: await context.newPage(), url, outDir, slug, band });
  } finally {
    await context.close();
  }
}
