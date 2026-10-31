// The app's screens, read from the router that defines them. `web/src/main.tsx`
// is the single source: a screen is a createRoute call with a path and a
// component, and the component's import names the page file whose change
// chooses the route.

/** One screen the app declares: its router path, page file and shot file stem. */
export type Route = {
  // The router's own path, e.g. "/runs/$runId".
  pattern: string;
  // The page file the route renders, e.g. "web/src/pages/run.tsx", or null for
  // a route named on the command line that the router does not define.
  page: string | null;
  // The URL to open: the pattern with its parameters filled in.
  url: string;
  // The file-name stem, e.g. "runs-runId".
  name: string;
};

/**
 * The values the sample URLs use for a route's parameters. A parameter without
 * one is a page the tool cannot open, and it says so rather than guessing.
 */
export const SAMPLE_PARAMS: Readonly<Record<string, string>> = {
  runId: "run-2026-02-18-a",
};

const LOCAL_IMPORT = /^import\s*\{([^}]*)\}\s*from\s*"\.\/([^"]+)";$/gm;
const ROUTE_BLOCK = /createRoute\(\{[\s\S]*?\n\}\)/g;
const ROUTE_PATH = /\bpath:\s*"([^"]*)"/;
const ROUTE_COMPONENT = /\bcomponent:\s*(\w+)/;

/**
 * routesFromRouter parses a router module into its component routes, in the
 * order the file declares them. A route without a component (a redirect) is
 * not a screen and is skipped.
 */
export function routesFromRouter(source: string): Route[] {
  const pages = new Map<string, string>();
  for (const match of source.matchAll(LOCAL_IMPORT)) {
    const names = match[1];
    const module = match[2];
    if (names === undefined || module === undefined) {
      continue;
    }
    for (const name of names.split(",")) {
      const component = name.trim();
      if (component !== "") {
        pages.set(component, `web/src/${module}.tsx`);
      }
    }
  }

  const routes: Route[] = [];
  for (const match of source.matchAll(ROUTE_BLOCK)) {
    const block = match[0];
    const pattern = ROUTE_PATH.exec(block)?.[1];
    const component = ROUTE_COMPONENT.exec(block)?.[1];
    if (pattern === undefined || component === undefined) {
      continue;
    }
    routes.push({
      pattern,
      page: pages.get(component) ?? null,
      url: routeUrl(pattern),
      name: routeName(pattern),
    });
  }
  return routes;
}

/**
 * routeUrl fills a pattern's parameters; a new parameter without a sample value
 * is an error naming the route, not a URL that silently omits it.
 */
export function routeUrl(pattern: string, samples: Readonly<Record<string, string>> = SAMPLE_PARAMS): string {
  const missing: string[] = [];
  const url = pattern.replace(/\$(\w+)/g, (_, name: string) => {
    const value = samples[name];
    if (value === undefined) {
      missing.push(name);
      return "";
    }
    return encodeURIComponent(value);
  });
  if (missing.length > 0) {
    throw new Error(
      `route ${pattern} has parameter(s) ${missing.join(", ")} with no sample value; ` +
        "add one to SAMPLE_PARAMS in tools/shots/router.ts",
    );
  }
  return url;
}

/**
 * routeName is the file-name stem of a route: "/" is "root", and the rest lose
 * their slashes and parameter markers. Every name stays within one path segment.
 */
export function routeName(pattern: string): string {
  if (pattern === "/") {
    return "root";
  }
  return pattern
    .replace(/^\/+/, "")
    .replace(/\/+$/, "")
    .replace(/[/$]+/g, "-")
    .replace(/-+/g, "-");
}

/**
 * routesForNames resolves the comma-separated routes a caller named. A name that
 * matches a router pattern takes that route (and its page); anything else is
 * opened as a URL, so a route whose page the router does not define still shoots.
 */
export function routesForNames(names: readonly string[], known: readonly Route[]): Route[] {
  return names.map((name) => {
    const pattern = known.find((route) => route.pattern === name);
    if (pattern !== undefined) {
      return pattern;
    }
    if (!name.startsWith("/")) {
      throw new Error(`route ${name} is not a path; it must start with "/"`);
    }
    return {
      pattern: name,
      page: null,
      url: name,
      name: routeName(name),
    };
  });
}

/**
 * routesForChange is the default route set: the screens whose page file
 * changed, in router order, or every route when none did.
 */
export function routesForChange(
  routes: readonly Route[],
  changedPages: ReadonlySet<string>,
): { routes: Route[]; changed: boolean } {
  const changed = routes.filter((route) => route.page !== null && changedPages.has(route.page));
  return changed.length > 0 ? { routes: changed, changed: true } : { routes: [...routes], changed: false };
}
