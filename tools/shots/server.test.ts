import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { startServer, servePath, type ShotServer } from "./server";

const cleanup: string[] = [];

afterAll(() => {
  for (const dir of cleanup) {
    rmSync(dir, { recursive: true, force: true });
  }
});

function tempDir(files: Record<string, string | Uint8Array>): string {
  const dir = mkdtempSync(join(tmpdir(), "shots-server-"));
  cleanup.push(dir);
  for (const [name, body] of Object.entries(files)) {
    const path = join(dir, name);
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, body);
  }
  return dir;
}

describe("startServer", () => {
  let server: ShotServer;

  beforeAll(() => {
    const dist = tempDir({
      "index.html": '<div id="root">app</div>',
      "assets/app.js": "export const a = 1;",
      "fonts/font.woff2": new Uint8Array([0x77, 0x4f, 0x46, 0x32]),
    });
    const fixtures = tempDir({ "inbox.json": '{"runs":[{"run":"r1"}]}' });
    server = startServer(dist, fixtures);
  });

  afterAll(() => server.stop());

  test("serves the built index and an asset", async () => {
    const index = await fetch(`${server.url}/`);
    expect(index.status).toBe(200);
    expect(await index.text()).toContain("app");

    const asset = await fetch(`${server.url}/assets/app.js`);
    expect(asset.status).toBe(200);
    expect(asset.headers.get("content-type")).toContain("javascript");
    expect(await asset.text()).toBe("export const a = 1;");
  });

  test("falls back to the SPA for a path without an extension", async () => {
    const page = await fetch(`${server.url}/stats`);
    expect(await page.text()).toContain("app");
  });

  test("404s a missing asset instead of serving the SPA", async () => {
    const missing = await fetch(`${server.url}/assets/nope.js`);
    expect(missing.status).toBe(404);
  });

  test("answers a fixture route with its JSON, query string ignored", async () => {
    const answer = await fetch(`${server.url}/api/inbox?since=1`);
    expect(answer.status).toBe(200);
    expect(answer.headers.get("content-type")).toContain("application/json");
    expect(await answer.text()).toBe('{"runs":[{"run":"r1"}]}');
    expect(server.fixtures.missing.size).toBe(0);
  });

  test("404s and records a route no fixture answers", async () => {
    const answer = await fetch(`${server.url}/api/autonomy?repo=acme/widgets`);
    expect(answer.status).toBe(404);
    expect([...server.fixtures.missing]).toEqual([["/api/autonomy", 1]]);
  });

  test("does not accept a write to the fixture API, and records it", async () => {
    const answer = await fetch(`${server.url}/api/inbox`, { method: "POST", body: "{}" });
    expect(answer.status).toBe(405);
    expect([...server.writes]).toEqual([["POST /api/inbox", 1]]);
  });
});

describe("servePath", () => {
  // A request path always starts with "/", which the URL parser has already
  // resolved, so these guards are only reachable with the raw paths given here.
  test("never resolves a path that climbs out of dist", () => {
    const root = tempDir({ "secret.txt": "secret" });
    const dist = join(root, "dist");
    mkdirSync(dist);
    writeFileSync(join(dist, "index.html"), "app");

    expect(servePath(dist, "../secret.txt")).toBeNull();
  });

  test("refuses a path with a NUL byte", () => {
    const dist = tempDir({ "index.html": "app" });
    expect(servePath(dist, "/stats\0")).toBeNull();
  });

  test("falls back to index.html for a path without an extension", () => {
    const dist = tempDir({ "index.html": "app" });
    expect(servePath(dist, "/stats")).toBe(join(dist, "index.html"));
  });
});
