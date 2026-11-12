// bun run shots: serve the built surface with fixture data and shoot each route
// at the four review widths. The route set defaults to the screens whose page
// file changed since the base, so a run shows what it touched.

import { existsSync, mkdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { join, resolve } from "node:path";
import { captureShot, FIXED_TIME } from "./capture";
import { changedPages } from "./changed";
import { checkStrings, type StringChecks } from "./checks";
import { parseArgs, type Args, USAGE, UsageError } from "./cli";
import { findChromium, launchChromium, openPage } from "./chromium";
import { readCriteria } from "./criteria";
import { type Overflow, outcome, type PageError, type Refusal } from "./report";
import { routesForChange, routesForNames, routesFromRouter, type Route } from "./router";
import { startServer } from "./server";
import { shootRoutes } from "./shoot";

const REPO_ROOT = resolve(import.meta.dir, "../..");
const ROUTER = join(REPO_ROOT, "web/src/main.tsx");

/**
 * run does the work and answers the exit code: 0 when every shot was written,
 * 1 when a fixture was missing, a write went unanswered, a shot was refused, a
 * route's page logged an error, a quoted string was missing, an element was
 * wider than its box, or the tool could not start.
 */
export async function run(argv: readonly string[]): Promise<number> {
  let args: Args;
  try {
    args = parseArgs(argv);
  } catch (err) {
    if (err instanceof UsageError) {
      console.error(`error: ${err.message}\n\n${USAGE}`);
      return 1;
    }
    console.error(`error: ${err instanceof Error ? err.message : String(err)}`);
    return 1;
  }
  if (args.help) {
    console.log(USAGE);
    return 0;
  }
  try {
    return await execute(args);
  } catch (err) {
    console.error(`error: ${err instanceof Error ? err.message : String(err)}`);
    return 1;
  }
}

// execute runs a parsed command line to its exit code, throwing when the tool
// cannot get as far as reporting the shots it took.
async function execute(args: Args): Promise<number> {
  const dist = args.dist === null ? join(REPO_ROOT, "web/dist") : resolve(args.dist);
  const fixtures =
    args.fixtures === null ? join(REPO_ROOT, "web/fixtures") : resolve(args.fixtures);
  const out = resolve(args.out);
  for (const [flag, dir] of [
    ["--dist", dist],
    ["--fixtures", fixtures],
  ] as const) {
    if (!existsSync(dir) || !statSync(dir).isDirectory()) {
      throw new Error(`${flag} ${dir} is not a directory`);
    }
  }
  if (!existsSync(join(dist, "index.html"))) {
    throw new Error(`${dist} has no index.html; run \`bun run build\` in web/ first`);
  }
  mkdirSync(out, { recursive: true });
  const criteria = args.ticket === null ? null : readCriteria(resolve(args.ticket));

  const routes = routesFromRouter(readFileSync(ROUTER, "utf8"));
  if (routes.length === 0) {
    throw new Error(`no component routes in ${ROUTER}; has the router changed shape?`);
  }
  let chosen: Route[];
  if (args.routes !== null) {
    chosen = routesForNames(args.routes, routes);
    console.log(
      `shotsSelected source=routes count=${chosen.length} ` +
        `routes=${chosen.map((route) => route.pattern).join(",")}`,
    );
  } else {
    const selection = routesForChange(routes, changedPages(REPO_ROOT, args.base));
    chosen = selection.routes;
    console.log(
      `shotsSelected source=${selection.changed ? "changed-pages" : "every-route"} ` +
        `base=${args.base} count=${chosen.length} ` +
        `routes=${chosen.map((route) => route.pattern).join(",")}`,
    );
  }

  const server = startServer(dist, fixtures);
  let written = 0;
  let refusals: Refusal[] = [];
  let pageErrors: PageError[] = [];
  let overflows: Overflow[] = [];
  let strings: StringChecks | null = null;
  try {
    if (args.chromium !== null && !existsSync(args.chromium)) {
      throw new Error(`--chromium ${args.chromium} does not exist`);
    }
    const executable = findChromium(args.chromium);
    if (executable === null) {
      throw new Error("no Chromium found; pass --chromium <path> or set CHROME_PATH to one");
    }
    const browser = await launchChromium(executable);
    try {
      const page = await openPage(browser.cdp, FIXED_TIME);
      const shots = await shootRoutes({
        routes: chosen,
        baseUrl: server.url,
        out,
        page,
        log: (line) => console.log(line),
        capture: (shot) => captureShot({ cdp: browser.cdp, page, ...shot }),
      });
      written = shots.written;
      refusals = shots.refusals;
      pageErrors = shots.pageErrors;
      overflows = shots.overflows;
      strings = criteria === null ? null : checkStrings(criteria, shots.pages, routes);
    } finally {
      await browser.stop();
    }
  } finally {
    server.stop();
  }

  if (criteria !== null && !criteria.some((criterion) => criterion.strings.length > 0)) {
    console.log("shotsUnchecked reason=no-criteria");
  }
  if (strings !== null) {
    writeFileSync(join(out, "checks.json"), `${JSON.stringify(strings, null, 2)}\n`);
    for (const skipped of strings.unchecked) {
      console.log(
        `shotsUnchecked reason=${skipped.reason} item=${skipped.item} ` +
          `string=${JSON.stringify(skipped.string)}`,
      );
    }
  }
  const result = outcome(server.fixtures.missing, server.writes, refusals, pageErrors, {
    missingStrings: strings?.missing ?? [],
    overflows,
  });
  if (result.failures.length > 0) {
    for (const line of result.failures) {
      console.error(line);
    }
  } else {
    console.log(`shotsDone count=${written} out=${out}`);
  }
  return result.exitCode;
}

if (import.meta.main) {
  run(process.argv.slice(2))
    .then((code) => {
      process.exitCode = code;
    })
    .catch((err: unknown) => {
      console.error(`error: ${err instanceof Error ? err.message : String(err)}`);
      process.exitCode = 1;
    });
}
