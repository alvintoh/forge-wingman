/**
 * The command line `bun run shots` takes. Paths stay as typed here; the entry
 * point resolves them, defaults against the repository root and explicit ones
 * against the directory the command ran in.
 */
export type Args = {
  // Where the built SPA is; null means <repo>/web/dist.
  dist: string | null;
  // Where the fixture JSON files are; null means <repo>/web/fixtures.
  fixtures: string | null;
  // Where the PNGs go; required.
  out: string;
  // The routes to shoot, as given; null takes the changed page files.
  routes: string[] | null;
  // The commit the changed page files are diffed against.
  base: string;
  // A Chromium binary; null means $CHROME_PATH or the usual install paths.
  chromium: string | null;
  help: boolean;
};

/** The usage text `--help` prints. */
export const USAGE = `usage: bun run shots --out <dir> [options]

Shoots the built surface at four widths with fixture data.

  --out <dir>        where the PNGs are written (required)
  --routes <list>    comma-separated routes, e.g. "/,/stats"
                     (default: the routes whose page file changed since --base,
                     else every route)
  --base <ref>       the commit to diff page files against (default: main)
  --dist <dir>       the built SPA (default: web/dist)
  --fixtures <dir>   the fixture JSON files (default: web/fixtures)
  --chromium <path>  the Chromium binary (default: $CHROME_PATH, then the
                     usual install paths)
  --help             this text`;

/** A command line the tool cannot run. */
export class UsageError extends Error {}

const FLAGS = new Set(["--dist", "--fixtures", "--out", "--routes", "--base", "--chromium"]);

/**
 * parseArgs reads the flags in any order, each as `--flag value` or
 * `--flag=value`, and rejects anything it does not know rather than ignoring it.
 */
export function parseArgs(argv: readonly string[]): Args {
  const args: Args = {
    dist: null,
    fixtures: null,
    out: "",
    routes: null,
    base: "main",
    chromium: null,
    help: false,
  };
  for (let i = 0; i < argv.length; i++) {
    const token = argv[i];
    if (token === "--help" || token === "-h") {
      args.help = true;
      continue;
    }
    const [flag, inline] = split(token);
    if (!FLAGS.has(flag)) {
      throw new UsageError(`unknown flag ${flag}`);
    }
    const value = inline ?? argv[++i];
    if (value === undefined || value === "") {
      throw new UsageError(`${flag} needs a value`);
    }
    switch (flag) {
      case "--dist":
        args.dist = value;
        break;
      case "--fixtures":
        args.fixtures = value;
        break;
      case "--out":
        args.out = value;
        break;
      case "--routes":
        args.routes = value
          .split(",")
          .map((route) => route.trim())
          .filter((route) => route !== "");
        if (args.routes.length === 0) {
          throw new UsageError("--routes needs at least one route");
        }
        break;
      case "--base":
        args.base = value;
        break;
      case "--chromium":
        args.chromium = value;
        break;
    }
  }
  if (!args.help && args.out === "") {
    throw new UsageError("--out is required");
  }
  return args;
}

function split(token: string): [string, string | undefined] {
  const at = token.indexOf("=");
  return at === -1 ? [token, undefined] : [token.slice(0, at), token.slice(at + 1)];
}
