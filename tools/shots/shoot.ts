// The route loop: every route at every width, through a capture function the
// caller supplies, so the refusal handling is driven without a browser.

import { join } from "node:path";
import { CHECK_VIEWPORT, type CaptureResult, Refused, VIEWPORTS, type Viewport } from "./capture";
import type { PageText } from "./checks";
import type { Page } from "./chromium";
import type { Overflow, PageError, Refusal } from "./report";
import type { Route } from "./router";

/** A capture of one route at one width, as shootRoutes drives it. */
export type Capture = (shot: {
  url: string;
  viewport: Viewport;
  out: string;
}) => Promise<CaptureResult>;

/** What shootRoutes needs: the routes, where they are served, and where the PNGs go. */
export type ShootOptions = {
  routes: readonly Route[];
  // The origin the fixture server answers on.
  baseUrl: string;
  // The directory the PNGs are written into.
  out: string;
  capture: Capture;
  // The widths to visit; the four review widths and the check-only one when omitted.
  viewports?: readonly Viewport[];
  // The page the capture drives, whose console errors fail the route they came from.
  page?: Page;
  // Called once per shot written, with a countable line.
  log?: (line: string) => void;
};

/**
 * What shootRoutes did: the shots written, the shots it refused, the pages that
 * logged, the text each state held and the elements wider than their boxes.
 */
export type ShootResult = {
  written: number;
  refusals: Refusal[];
  pageErrors: PageError[];
  pages: PageText[];
  overflows: Overflow[];
};

/**
 * shotFile places one shot in its width's folder, named after its route:
 * `s/stats.png`. The root is `index`, the name the runner's evidence step reads
 * as a folder's own page (internal/runner/evidence.go, shotRoute).
 */
export function shotFile(route: Route, viewport: Viewport): string {
  const stem = route.pattern === "/" ? "index" : route.name;
  return `${viewport.key}/${stem}.png`;
}

/**
 * shootRoutes writes every route at every width, collecting the shots a
 * refusal stopped and the errors a route's page logged, and letting every
 * other error through.
 */
export async function shootRoutes(options: ShootOptions): Promise<ShootResult> {
  const viewports = options.viewports ?? [...VIEWPORTS, CHECK_VIEWPORT];
  const log = options.log ?? (() => {});
  const refusals: Refusal[] = [];
  const pageErrors: PageError[] = [];
  const pages: PageText[] = [];
  const overflows: Overflow[] = [];
  let written = 0;
  for (const route of options.routes) {
    for (const viewport of viewports) {
      const name = shotFile(route, viewport);
      try {
        const seen = await options.capture({
          url: options.baseUrl + route.url,
          viewport,
          out: join(options.out, name),
        });
        pages.push({ route: route.pattern, width: viewport.key, text: seen.text });
        for (const element of seen.overflows) {
          overflows.push({ route: route.pattern, width: viewport.key, ...element });
        }
        if (!viewport.checkOnly) {
          written++;
          log(`shotWrote file=${name} width=${seen.width} height=${seen.height}`);
        }
      } catch (err) {
        if (err instanceof Refused) {
          refusals.push({ route: route.name, width: viewport.key, detail: err.message });
          for (const element of err.overflows) {
            overflows.push({ route: route.pattern, width: viewport.key, ...element });
          }
          continue;
        }
        throw err;
      }
    }
    if (options.page !== undefined && options.page.errors.length > 0) {
      pageErrors.push({ route: route.pattern, detail: options.page.errors.join(" | ") });
      options.page.errors.length = 0;
    }
  }
  return { written, refusals, pageErrors, pages, overflows };
}
