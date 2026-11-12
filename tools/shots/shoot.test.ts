import { describe, expect, test } from "bun:test";
import { CHECK_VIEWPORT, type CaptureResult, Refused, type Viewport } from "./capture";
import type { Page } from "./chromium";
import { outcome } from "./report";
import type { Route } from "./router";
import { shootRoutes, shotFile } from "./shoot";

const WIDE: Viewport = { key: "l", width: 1024, height: 768, deviceScaleFactor: 1, mobile: false };
const ROUTES: Route[] = [
  { pattern: "/", page: null, url: "/", name: "root" },
  { pattern: "/stats", page: null, url: "/stats", name: "stats" },
];

function seen(overrides: Partial<CaptureResult> = {}): CaptureResult {
  return { width: 1024, height: 768, text: "", overflows: [], ...overrides };
}

function page(): Page {
  return { sessionId: "s", pending: new Set(), errors: [] };
}

describe("shotFile", () => {
  test("places a shot in its width's folder, named after its route", () => {
    expect(shotFile(ROUTES[1]!, WIDE)).toBe("l/stats.png");
  });

  test("names the root index, the runner's name for a folder's own page", () => {
    expect(shotFile(ROUTES[0]!, WIDE)).toBe("l/index.png");
  });
});

describe("shootRoutes", () => {
  test("writes every route at every width and counts them", async () => {
    const written: string[] = [];
    const urls: string[] = [];
    const result = await shootRoutes({
      routes: ROUTES,
      baseUrl: "http://127.0.0.1:9",
      out: "/tmp/shots-test",
      viewports: [WIDE],
      capture: async (shot) => {
        written.push(shot.out);
        urls.push(shot.url);
        return seen();
      },
    });
    expect(result).toEqual({
      written: 2,
      refusals: [],
      pageErrors: [],
      pages: [
        { route: "/", width: "l", text: "" },
        { route: "/stats", width: "l", text: "" },
      ],
      overflows: [],
    });
    expect(urls).toEqual(["http://127.0.0.1:9/", "http://127.0.0.1:9/stats"]);
    expect(written).toEqual(["/tmp/shots-test/l/index.png", "/tmp/shots-test/l/stats.png"]);
  });

  test("collects a refusal and carries on with the remaining shots", async () => {
    const calls: string[] = [];
    const result = await shootRoutes({
      routes: ROUTES,
      baseUrl: "http://127.0.0.1:9",
      out: "/tmp/shots-test",
      viewports: [WIDE],
      capture: async (shot) => {
        calls.push(shot.out);
        if (shot.url.endsWith("/stats")) {
          throw new Refused("the page scrolls sideways: 12px wider than the viewport");
        }
        return seen();
      },
    });
    expect(result.written).toBe(1);
    expect(result.refusals).toEqual([
      {
        route: "stats",
        width: "l",
        detail: "the page scrolls sideways: 12px wider than the viewport",
      },
    ]);
    expect(calls).toEqual(["/tmp/shots-test/l/index.png", "/tmp/shots-test/l/stats.png"]);
  });

  test("collects a route's page errors and clears them for the next route", async () => {
    const driven = page();
    const result = await shootRoutes({
      routes: ROUTES,
      baseUrl: "http://127.0.0.1:9",
      out: "/tmp/shots-test",
      viewports: [WIDE],
      page: driven,
      capture: async (shot) => {
        if (shot.url.endsWith("/stats")) {
          driven.errors.push(
            "comparisonNotRead",
            "the endpoint answered a document of another shape",
          );
        }
        return seen();
      },
    });
    expect(result.pageErrors).toEqual([
      {
        route: "/stats",
        detail: "comparisonNotRead | the endpoint answered a document of another shape",
      },
    ]);
    expect(driven.errors).toEqual([]);
    expect(outcome(new Map(), new Map(), result.refusals, result.pageErrors)).toEqual({
      failures: [
        'shotsFailed reason=page-error route=/stats detail="comparisonNotRead | the endpoint answered a document of another shape"',
      ],
      exitCode: 1,
    });
  });

  test("lets a capture error that is not a refusal through", async () => {
    await expect(
      shootRoutes({
        routes: ROUTES,
        baseUrl: "http://127.0.0.1:9",
        out: "/tmp/shots-test",
        viewports: [WIDE],
        capture: async () => {
          throw new Error("the browser closed");
        },
      }),
    ).rejects.toThrow("the browser closed");
  });

  test("logs each shot with a fixed token and separate fields", async () => {
    const lines: string[] = [];
    await shootRoutes({
      routes: [ROUTES[0]!],
      baseUrl: "http://127.0.0.1:9",
      out: "/tmp/shots-test",
      viewports: [WIDE],
      log: (line) => lines.push(line),
      capture: async () => seen(),
    });
    expect(lines).toEqual(["shotWrote file=l/index.png width=1024 height=768"]);
  });

  test("collects each state's text and the elements wider than their boxes", async () => {
    const result = await shootRoutes({
      routes: [ROUTES[1]!],
      baseUrl: "http://127.0.0.1:9",
      out: "/tmp/shots-test",
      viewports: [WIDE],
      capture: async () =>
        seen({
          text: "Runs from all projects",
          overflows: [{ element: "div.card-frame", scrollWidth: 512, clientWidth: 470 }],
        }),
    });
    expect(result.pages).toEqual([{ route: "/stats", width: "l", text: "Runs from all projects" }]);
    expect(result.overflows).toEqual([
      {
        route: "/stats",
        width: "l",
        element: "div.card-frame",
        scrollWidth: 512,
        clientWidth: 470,
      },
    ]);
  });

  test("visits the check-only width after the four review widths and shoots nothing there", async () => {
    const lines: string[] = [];
    const visited: string[] = [];
    const result = await shootRoutes({
      routes: [ROUTES[1]!],
      baseUrl: "http://127.0.0.1:9",
      out: "/tmp/shots-test",
      log: (line) => lines.push(line),
      capture: async (shot) => {
        visited.push(shot.viewport.key);
        return seen({ width: shot.viewport.width });
      },
    });
    expect(visited).toEqual(["s", "m", "l", "xl", "xs"]);
    expect(result.written).toBe(4);
    expect(result.pages.map((state) => state.width)).toEqual(["s", "m", "l", "xl", "xs"]);
    expect(lines.some((line) => line.includes("xs/"))).toBe(false);
  });

  test("collects the elements a refusal named, with the route and width they were seen at", async () => {
    const result = await shootRoutes({
      routes: [ROUTES[1]!],
      baseUrl: "http://127.0.0.1:9",
      out: "/tmp/shots-test",
      viewports: [WIDE],
      capture: async () => {
        const refusal = new Refused("the page scrolls sideways: 12px wider than the viewport");
        refusal.overflows = [{ element: "div.card-frame", scrollWidth: 2000, clientWidth: 1024 }];
        throw refusal;
      },
    });
    expect(result.overflows).toEqual([
      {
        route: "/stats",
        width: "l",
        element: "div.card-frame",
        scrollWidth: 2000,
        clientWidth: 1024,
      },
    ]);
  });

  test("collects a refusal at the check-only width like any other", async () => {
    const result = await shootRoutes({
      routes: [ROUTES[1]!],
      baseUrl: "http://127.0.0.1:9",
      out: "/tmp/shots-test",
      viewports: [CHECK_VIEWPORT],
      capture: async () => {
        throw new Refused("the page scrolls sideways: 12px wider than the viewport");
      },
    });
    expect(result.refusals).toEqual([
      {
        route: "stats",
        width: "xs",
        detail: "the page scrolls sideways: 12px wider than the viewport",
      },
    ]);
    expect(result.pages).toEqual([]);
  });
});
