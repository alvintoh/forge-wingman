// The route loop: every route at every width, through a capture function the
// caller supplies, so the refusal handling is driven without a browser.

import { join } from "node:path";
import { Refused, VIEWPORTS, type Viewport } from "./capture";
import type { Page } from "./chromium";
import type { PageError, Refusal } from "./report";
import type { Route } from "./router";

/** A capture of one route at one width, as shootRoutes drives it. */
export type Capture = (shot: {
  url: string;
  viewport: Viewport;
  out: string;
}) => Promise<{ width: number; height: number }>;

/** What shootRoutes needs: the routes, where they are served, and where the PNGs go. */
export type ShootOptions = {
  routes: readonly Route[];
  // The origin the fixture server answers on.
  baseUrl: string;
  // The directory the PNGs are written into.
  out: string;
  capture: Capture;
  // The widths to shoot; the four review widths when omitted.
  viewports?: readonly Viewport[];
  // The page the capture drives, whose console errors fail the route they came from.
  page?: Page;
  // Called once per shot written, with a countable line.
  log?: (line: string) => void;
};

/** What shootRoutes did: the shots written, the shots it refused and the pages that logged. */
export type ShootResult = {
  written: number;
  refusals: Refusal[];
  pageErrors: PageError[];
};

/** shotFile names one shot after its route and width: `stats-s.png`. */
export function shotFile(route: Route, viewport: Viewport): string {
  return `${route.name}-${viewport.key}.png`;
}

/**
 * shootRoutes writes every route at every width, collecting the shots a
 * refusal stopped and the errors a route's page logged, and letting every
 * other error through.
 */
export async function shootRoutes(options: ShootOptions): Promise<ShootResult> {
  const viewports = options.viewports ?? VIEWPORTS;
  const log = options.log ?? (() => {});
  const refusals: Refusal[] = [];
  const pageErrors: PageError[] = [];
  let written = 0;
  for (const route of options.routes) {
    for (const viewport of viewports) {
      const name = shotFile(route, viewport);
      try {
        const size = await options.capture({
          url: options.baseUrl + route.url,
          viewport,
          out: join(options.out, name),
        });
        written++;
        log(`shotWrote file=${name} width=${size.width} height=${size.height}`);
      } catch (err) {
        if (err instanceof Refused) {
          refusals.push({ route: route.name, width: viewport.key, detail: err.message });
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
  return { written, refusals, pageErrors };
}
