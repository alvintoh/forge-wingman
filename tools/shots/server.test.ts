import { afterAll, describe, expect, test } from "bun:test";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { type ShotServer, startServer } from "./server";

const work = await mkdtemp(join(tmpdir(), "shots-server-"));
const dist = join(work, "dist");
const fixtures = join(work, "fixtures");

await mkdir(join(dist, "assets"), { recursive: true });
await writeFile(join(dist, "index.html"), "<!doctype html><div id=root></div>");
await writeFile(join(dist, "assets", "app-3f2a.js"), "console.log('app');");
await mkdir(fixtures, { recursive: true });
await writeFile(join(fixtures, "inbox.json"), '{"runs":[]}');
await writeFile(join(fixtures, "runs-per-repo.json"), '{"runs_per_repo":5}');
await mkdir(join(fixtures, "projects"), { recursive: true });
await writeFile(join(fixtures, "projects", "options.json"), '{"names":["Forge Wingman"]}');

const server: ShotServer = startServer({ dist, fixtures });

afterAll(async () => {
  server.stop();
  await rm(work, { recursive: true, force: true });
});

const get = (path: string): Promise<Response> => fetch(`${server.url}${path}`);

describe("the fixture api", () => {
  test("answers a route with the file of its path", async () => {
    const response = await get("/api/inbox");
    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toBe("application/json; charset=utf-8");
    expect(await response.json()).toEqual({ runs: [] });
  });

  test("answers a nested route from its directory", async () => {
    expect(await (await get("/api/projects/options")).json()).toEqual({
      names: ["Forge Wingman"],
    });
  });

  test("answers a route the same whatever the query", async () => {
    expect(await (await get("/api/runs-per-repo?project=x")).json()).toEqual({
      runs_per_repo: 5,
    });
  });

  test("records a route it has no fixture for, and answers 404", async () => {
    const response = await get("/api/autonomy");
    expect(response.status).toBe(404);
    expect(server.missing()).toContain("/api/autonomy");
  });

  test("lists a missing route once however often it is read", async () => {
    await get("/api/autonomy?repo=alvintoh/forge-wingman");
    expect(server.missing().filter((route) => route === "/api/autonomy")).toHaveLength(1);
  });

  test("records only the routes it could not answer", () => {
    expect(server.missing()).toEqual(["/api/autonomy"]);
  });

  test("refuses a path that climbs out of the fixtures", async () => {
    const escaped = join(work, "secret.json");
    await writeFile(escaped, '{"secret":true}');
    const response = await get("/api/%2e%2e%2fsecret.json");
    expect(response.status).toBe(404);
    expect(await response.text()).not.toContain('"secret":true');
  });
});

describe("the built surface", () => {
  test("serves the build's own files", async () => {
    const response = await get("/assets/app-3f2a.js");
    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toBe("text/javascript;charset=utf-8");
    expect(await response.text()).toBe("console.log('app');");
  });

  test("answers a route from the index document", async () => {
    const response = await get("/stats");
    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toBe("text/html; charset=utf-8");
    expect(await response.text()).toContain('id=root');
  });

  test("answers 404 for a build file the build never wrote", async () => {
    const response = await get("/assets/app-0000.js");
    expect(response.status).toBe(404);
    expect(await response.text()).not.toContain("id=root");
  });

  test("serves a path that climbs out of the build nothing", async () => {
    expect((await get("/%2e%2e%2fREADME.md")).status).toBe(404);
  });
});
