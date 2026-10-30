import { mkdir } from "node:fs/promises";
import { join, relative } from "node:path";
import type { Browser } from "playwright";
import { type Options, parseArgs, USAGE, UsageError } from "./args";
import { BANDS } from "./bands";
import type { CaptureOptions, CaptureResult } from "./capture";
import { concretePath, type Selection, UnknownRoute, selectSurfaces, shotSlug } from "./routes";
import { startServer } from "./server";

/** What the tool reaches outside itself, so its decisions can be driven without a browser. */
export type Env = {
  /** The repository the options' defaults are read from. */
  root: string;
  launch: () => Promise<Browser>;
  capture: (options: CaptureOptions) => Promise<CaptureResult>;
  /** git with its arguments, in this repository, or null when git could not answer. */
  git: (args: readonly string[]) => string | null;
  log: (line: string) => void;
  error: (line: string) => void;
};

/**
 * changedPages is every file differing from base: committed, changed in the working tree, or added
 * and not yet staged — a page file a branch adds is untracked, and a diff never names it. A
 * repository, or a ref, git cannot read yields no pages, which selects every route rather than none.
 */
export function changedPages(git: Env["git"], base: string): string[] {
  const mergeBase =
    git(["merge-base", "HEAD", base])?.trim() || git(["merge-base", "HEAD", "main"])?.trim();
  if (!mergeBase) {
    return [];
  }
  const names = [
    git(["diff", "--name-only", mergeBase, "HEAD"]) ?? "",
    git(["diff", "--name-only", "HEAD"]) ?? "",
    git(["ls-files", "--others", "--exclude-standard"]) ?? "",
  ];
  return [
    ...new Set(
      names
        .join("\n")
        .split("\n")
        .map((name) => name.trim())
        .filter((name) => name !== ""),
    ),
  ];
}

/** firstRunId is the run the inbox fixture lists first, which /runs/$runId is captured at. */
export async function firstRunId(fixtures: string): Promise<string | null> {
  try {
    const body: unknown = await Bun.file(join(fixtures, "inbox.json")).json();
    const runs = (body as { runs?: unknown } | null)?.runs;
    if (!Array.isArray(runs)) {
      return null;
    }
    const run = (runs[0] as { run?: unknown } | undefined)?.run;
    return typeof run === "string" ? run : null;
  } catch {
    return null;
  }
}

/**
 * runShots captures the selected routes and returns the exit code: 0 with every shot written, 2 for
 * options or a route it cannot act on, 1 for anything that would leave the evidence short — no
 * build, a browser that will not start, a refused shot, or an api route the fixtures lack.
 */
export async function runShots(argv: readonly string[], env: Env): Promise<number> {
  let options: Options;
  try {
    options = parseArgs(argv, env.root, process.cwd());
  } catch (err) {
    if (!(err instanceof UsageError)) {
      throw err;
    }
    env.error(`shots: ${err.message}`);
    env.error(`\n${USAGE}`);
    return 2;
  }
  if (options.help) {
    env.log(USAGE);
    return 0;
  }

  const index = Bun.file(join(options.dist, "index.html"));
  if (!(await index.exists())) {
    env.error(
      `shots: ${options.dist} holds no index.html; build the surface first — \`bun run build\` in web/`,
    );
    return 1;
  }

  let selections: Selection[];
  try {
    selections =
      options.routes === null
        ? selectSurfaces(null, changedPages(env.git, options.base))
        : selectSurfaces(options.routes, []);
  } catch (err) {
    if (!(err instanceof UnknownRoute)) {
      throw err;
    }
    env.error(`shots: ${err.message}`);
    return 2;
  }

  const runId = await firstRunId(options.fixtures);
  await mkdir(options.out, { recursive: true });
  const server = startServer({ dist: options.dist, fixtures: options.fixtures });
  const failures: string[] = [];
  let browser: Browser;
  try {
    browser = await env.launch();
  } catch (err) {
    server.stop();
    env.error(`shots: ${(err as Error).message}`);
    return 1;
  }

  try {
    for (const { surface, path: named } of selections) {
      let path: string;
      try {
        path = named ?? concretePath(surface, runId);
      } catch (err) {
        // One route that cannot be captured is a shortfall of its own; the rest still are.
        env.error(`shots: ${(err as Error).message}`);
        failures.push(surface.route);
        continue;
      }
      const url = new URL(path, server.url).href;
      const slug = shotSlug(path);
      for (const band of BANDS) {
        const result = await env.capture({ browser, url, outDir: options.out, slug, band });
        if (result.ok) {
          const shown = relative(process.cwd(), result.path);
          env.log(`saved ${shown.startsWith("..") ? result.path : shown}`);
        } else {
          failures.push(`${path} at ${band.name}: ${result.reason}`);
        }
      }
    }
  } finally {
    await browser.close();
    server.stop();
  }

  const missing = server.missing();
  if (missing.length > 0) {
    env.error(
      `shots: the app read ${missing.length} api route(s) no fixture answers — add one file per ` +
        `route under ${options.fixtures}:`,
    );
    for (const route of missing) {
      env.error(`  ${route}`);
    }
  }
  if (failures.length > 0) {
    env.error(`shots: ${failures.length} shot(s) refused:`);
    for (const failure of failures) {
      env.error(`  ${failure}`);
    }
  }
  if (missing.length > 0 || failures.length > 0) {
    return 1;
  }

  env.log(
    `shots: ${selections.length * BANDS.length} shot(s) in ${options.out} from ` +
      `${selections.length} route(s)`,
  );
  return 0;
}
