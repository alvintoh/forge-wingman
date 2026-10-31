import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { routeName, routesForChange, routesForNames, routesFromRouter, routeUrl } from "./router";

const REPO_ROOT = join(import.meta.dir, "../..");
const ROUTER = readFileSync(join(REPO_ROOT, "web/src/main.tsx"), "utf8");

describe("routesFromRouter", () => {
  test("reads every screen the app declares, with its page file", () => {
    const routes = routesFromRouter(ROUTER);
    expect(routes.map((route) => route.pattern)).toEqual([
      "/",
      "/runs/$runId",
      "/stats",
      "/config",
      "/autonomy",
      "/projects",
    ]);
    expect(routes.map((route) => route.page)).toEqual([
      "web/src/pages/overview.tsx",
      "web/src/pages/run.tsx",
      "web/src/pages/stats.tsx",
      "web/src/pages/config.tsx",
      "web/src/pages/autonomy.tsx",
      "web/src/projects.tsx",
    ]);
  });

  test("skips a route that only redirects", () => {
    const source = `
import { Stats } from "./pages/stats";
const statsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/stats",
  component: Stats,
});
const oldRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/old",
  beforeLoad: toStats,
});
`;
    expect(routesFromRouter(source).map((route) => route.pattern)).toEqual(["/stats"]);
  });
});

describe("routeName", () => {
  test("names the root and the rest of the paths without slashes", () => {
    expect(routeName("/")).toBe("root");
    expect(routeName("/stats")).toBe("stats");
    expect(routeName("/runs/$runId")).toBe("runs-runId");
    expect(routeName("/")).not.toContain("/");
  });
});

describe("routeUrl", () => {
  test("fills a sample parameter", () => {
    expect(routeUrl("/runs/$runId")).toBe("/runs/run-2026-02-18-a");
  });

  test("names a parameter that has no sample value", () => {
    expect(() => routeUrl("/runs/$unknown")).toThrow("unknown");
  });
});

describe("routesForNames", () => {
  const routes = routesFromRouter(ROUTER);

  test("resolves a router pattern to its page", () => {
    const [stats] = routesForNames(["/stats"], routes);
    expect(stats?.page).toBe("web/src/pages/stats.tsx");
    expect(stats?.name).toBe("stats");
  });

  test("opens an unnamed path as a URL", () => {
    const [run] = routesForNames(["/runs/other"], routes);
    expect(run).toEqual({ pattern: "/runs/other", page: null, url: "/runs/other", name: "runs-other" });
  });

  test("rejects a name that is not a path", () => {
    expect(() => routesForNames(["stats"], routes)).toThrow('must start with "/"');
  });
});

describe("routesForChange", () => {
  const routes = routesFromRouter(ROUTER);

  test("keeps only routes whose page file changed", () => {
    const selection = routesForChange(
      routes,
      new Set(["web/src/pages/stats.tsx", "web/src/inbox/row.tsx"]),
    );
    expect(selection.changed).toBe(true);
    expect(selection.routes.map((route) => route.pattern)).toEqual(["/stats"]);
  });

  test("shoots every route when no page file changed", () => {
    const selection = routesForChange(routes, new Set(["web/src/inbox/row.tsx"]));
    expect(selection.changed).toBe(false);
    expect(selection.routes.map((route) => route.pattern)).toEqual(routes.map((route) => route.pattern));
  });
});
