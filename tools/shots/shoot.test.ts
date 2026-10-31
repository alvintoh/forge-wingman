import { describe, expect, test } from "bun:test";
import { Refused, type Viewport } from "./capture";
import type { Page } from "./chromium";
import { outcome } from "./report";
import type { Route } from "./router";
import { shootRoutes, shotFile } from "./shoot";

const WIDE: Viewport = { key: "l", width: 1024, height: 768, deviceScaleFactor: 1, mobile: false };
const ROUTES: Route[] = [
  { pattern: "/", page: null, url: "/", name: "root" },
  { pattern: "/stats", page: null, url: "/stats", name: "stats" },
];

function page(): Page {
  return { sessionId: "s", pending: new Set(), errors: [] };
}

describe("shotFile", () => {
  test("names a shot after its route and width", () => {
    expect(shotFile(ROUTES[1]!, WIDE)).toBe("stats-l.png");
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
        return { width: 1024, height: 768 };
      },
    });
    expect(result).toEqual({ written: 2, refusals: [], pageErrors: [] });
    expect(urls).toEqual(["http://127.0.0.1:9/", "http://127.0.0.1:9/stats"]);
    expect(written).toEqual(["/tmp/shots-test/root-l.png", "/tmp/shots-test/stats-l.png"]);
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
        return { width: 1024, height: 768 };
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
    expect(calls).toEqual(["/tmp/shots-test/root-l.png", "/tmp/shots-test/stats-l.png"]);
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
        return { width: 1024, height: 768 };
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
      capture: async () => ({ width: 1024, height: 768 }),
    });
    expect(lines).toEqual(["shotWrote file=root-l.png width=1024 height=768"]);
  });
});
