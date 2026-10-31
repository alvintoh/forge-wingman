import { afterAll, describe, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { isAutonomy, isRewrite } from "../../web/src/autonomy/guard";
import { isConfigView, isPeakView, isRunsPerRepoView } from "../../web/src/config/types";
import { isInbox } from "../../web/src/inbox/run";
import { FixtureApi, fixtureFile } from "./fixtures";

const REPO_ROOT = join(import.meta.dir, "../..");
const FIXTURES = join(REPO_ROOT, "web/fixtures");

const cleanup: string[] = [];

afterAll(() => {
  for (const dir of cleanup) {
    rmSync(dir, { recursive: true, force: true });
  }
});

function tempFixtures(files: Record<string, string>): string {
  const dir = mkdtempSync(join(tmpdir(), "shots-fixtures-"));
  cleanup.push(dir);
  for (const [name, body] of Object.entries(files)) {
    writeFileSync(join(dir, name), body);
  }
  return dir;
}

describe("fixtureFile", () => {
  test("maps a route to its file", () => {
    expect(fixtureFile("/api/inbox")).toBe("inbox.json");
    expect(fixtureFile("/api/projects/options")).toBe("projects-options.json");
    expect(fixtureFile("/api/runs-per-repo/")).toBe("runs-per-repo.json");
  });

  test("answers null outside the API", () => {
    expect(fixtureFile("/stats")).toBeNull();
    expect(fixtureFile("/api/")).toBeNull();
  });
});

describe("FixtureApi", () => {
  test("serves a fixture and records a route it lacks", () => {
    const api = new FixtureApi(tempFixtures({ "inbox.json": '{"runs":[]}' }));
    expect(api.get("/api/inbox")).toBe('{"runs":[]}');
    expect(api.get("/api/autonomy")).toBeNull();
    expect(api.get("/api/autonomy")).toBeNull();
    expect([...api.missing]).toEqual([["/api/autonomy", 2]]);
  });

  test("fails at startup on a fixture that is not JSON", () => {
    const dir = tempFixtures({ "inbox.json": "{not json" });
    expect(() => new FixtureApi(dir)).toThrow("is not valid JSON");
  });
});

describe("the committed fixtures", () => {
  // Every /api route the app reads must have one fixture file, and every /api
  // call site must be one the scan can classify: a read helper or a write
  // helper. A literal under any other call is an unclassified route, which the
  // tool would not catch before the shot.
  test("answer every API read and classify every API call", () => {
    const reads = new Set<string>();
    const writes = new Set<string>();
    const unknown: string[] = [];
    for (const file of readdirSync(join(REPO_ROOT, "web/src"), { recursive: true })) {
      const path = join(REPO_ROOT, "web/src", String(file));
      if (!/\.tsx?$/.test(path)) {
        continue;
      }
      const source = readFileSync(path, "utf8");
      for (const match of source.matchAll(/(\w+)\s*\(\s*["'`](\/api\/[^"'`$?]*)/g)) {
        const callee = match[1];
        const route = match[2];
        if (callee === undefined || route === undefined) {
          continue;
        }
        if (callee === "fetch" || callee === "useRead") {
          reads.add(route);
        } else if (callee === "postJSON" || callee === "post") {
          writes.add(route);
        } else {
          unknown.push(`${callee}(${route})`);
        }
      }
    }
    expect(unknown).toEqual([]);
    // A write is a route the fixture API refuses, so the scan must see them.
    expect([...writes]).toContain("/api/auto-merge");
    const present = readdirSync(FIXTURES);
    const missing = [...reads].filter((route) => {
      const name = fixtureFile(route);
      return name === null || !present.includes(name);
    });
    expect(missing).toEqual([]);
  });
});

// The app checks each endpoint's answer with a guard before it draws it, so a
// fixture the guard rejects would show the page's error state instead. These are
// the exported guards; an unexported one is covered by the page-error failure the
// tool now reports.
describe("the fixtures' shapes", () => {
  const fixture = (name: string) =>
    JSON.parse(readFileSync(join(FIXTURES, `${name}.json`), "utf8"));

  test("autonomy passes the page's auto-merge and rewrite guards", () => {
    const autonomy = fixture("autonomy");
    expect(isAutonomy(autonomy)).toBe(true);
    expect(isRewrite(autonomy.rewrite)).toBe(true);
  });

  test("config, peak and runs-per-repo pass their guards", () => {
    expect(isConfigView(fixture("config"))).toBe(true);
    expect(isPeakView(fixture("peak"))).toBe(true);
    expect(isRunsPerRepoView(fixture("runs-per-repo"))).toBe(true);
  });

  test("inbox passes its guard", () => {
    expect(isInbox(fixture("inbox"))).toBe(true);
  });
});
