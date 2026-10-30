import { resolve } from "node:path";

/** Options is what `bun run shots` was asked to do. */
export type Options = {
  /** The routes to capture, or null for the ones whose page changed since the base. */
  routes: string[] | null;
  /** Where the shots are written. */
  out: string;
  /** The built surface to serve. */
  dist: string;
  /** The directory holding one JSON file per `/api` route. */
  fixtures: string;
  /** The ref the changed page files are read against. */
  base: string;
  help: boolean;
};

/** UsageError is an argument the tool cannot act on. */
export class UsageError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "UsageError";
  }
}

export const USAGE = `bun run shots — capture the built surface at four widths from fixture data.

  --routes <list>   Routes to capture, comma-separated, as web/src/main.tsx declares them:
                    ${"/, /runs/$runId, /stats, /config, /autonomy, /projects"}.
                    A concrete path for a parameterised route (/runs/abc123) captures that path.
                    Default: the routes whose page file changed since --base, else every one.
  --out <dir>       Where the shots are written. Default: shots/ at the repository root.
  --dist <dir>      The built surface to serve. Default: web/dist.
  --fixtures <dir>  One JSON file per API route, by path under the api: /api/projects/options reads
                    projects/options.json. Default: web/fixtures.
  --base <ref>      The ref the changed page files are read against. Default: origin/main.
  -h, --help        This text.

Each route is captured at 440x763 (scale 3), 768x1024, 1024x768 and 1280x800, as
<route>-<s|m|l|xl>.png. A shot is refused, and the run exits 1, when the app could not read a
fixture it asked for, an aria-busy region or a font is still pending, a control is under the
pointer, or the page scrolls sideways.`;

/** parseArgs reads the command line; root is the repository, cwd what relative paths resolve to. */
export function parseArgs(
  argv: readonly string[],
  root: string,
  cwd: string,
): Options {
  const options: Options = {
    routes: null,
    out: resolve(root, "shots"),
    dist: resolve(root, "web/dist"),
    fixtures: resolve(root, "web/fixtures"),
    base: "origin/main",
    help: false,
  };
  const routes: string[] = [];
  let namedRoutes = false;

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index] ?? "";
    const equals = arg.indexOf("=");
    const flag = equals === -1 ? arg : arg.slice(0, equals);
    const inline = equals === -1 ? null : arg.slice(equals + 1);
    const value = (): string => {
      if (inline !== null) {
        return inline;
      }
      const next = argv[index + 1];
      if (next === undefined) {
        throw new UsageError(`${flag} needs a value`);
      }
      index += 1;
      return next;
    };

    switch (flag) {
      case "--routes": {
        namedRoutes = true;
        routes.push(
          ...value()
            .split(",")
            .map((route) => route.trim())
            .filter((route) => route !== ""),
        );
        break;
      }
      case "--out":
        options.out = resolve(cwd, value());
        break;
      case "--dist":
        options.dist = resolve(cwd, value());
        break;
      case "--fixtures":
        options.fixtures = resolve(cwd, value());
        break;
      case "--base":
        options.base = value();
        break;
      case "--help":
      case "-h":
        options.help = true;
        break;
      default:
        throw new UsageError(`unknown argument ${arg}`);
    }
  }

  if (namedRoutes) {
    if (routes.length === 0) {
      throw new UsageError("--routes needs at least one route");
    }
    options.routes = routes;
  }
  return options;
}
