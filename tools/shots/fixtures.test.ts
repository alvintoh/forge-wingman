import { describe, expect, test } from "bun:test";
import { readdir } from "node:fs/promises";
import { join } from "node:path";
import { isAutonomy, isRewrite } from "../../web/src/autonomy/guard";
import { isConfigView, isPeakView, isRunsPerRepoView } from "../../web/src/config/types";
import { waits } from "../../web/src/inbox/kind";
import { isInbox } from "../../web/src/inbox/run";

const ROOT = join(import.meta.dir, "../..");
const FIXTURES = join(ROOT, "web/fixtures");
const SRC = join(ROOT, "web/src");
const SURFACE = join(ROOT, "cmd/surface/main.go");

async function sources(dir: string): Promise<string[]> {
  const entries = await readdir(dir, { withFileTypes: true, recursive: true });
  return entries
    .filter((entry) => entry.isFile() && /\.tsx?$/.test(entry.name))
    .map((entry) => join(entry.parentPath, entry.name));
}

/**
 * apiCalls is every api path the app names, wherever it names it: a literal, a template's prefix or
 * a string a variable holds. A scan keyed on the call that reads it would miss the last two.
 */
async function apiCalls(): Promise<Set<string>> {
  const calls = new Set<string>();
  for (const file of await sources(SRC)) {
    const text = await Bun.file(file).text();
    for (const match of text.matchAll(/\/api\/[A-Za-z0-9/_-]*/g)) {
      const route = match[0];
      // `/api/runs/${id}` stops at the template, so what matched is a prefix and no route.
      if (route.endsWith("/")) {
        continue;
      }
      calls.add(route);
    }
  }
  return calls;
}

/** apiRoutes is what the surface serves, by method, read from its own router. */
async function apiRoutes(): Promise<{ reads: Set<string>; writes: Set<string> }> {
  const reads = new Set<string>();
  const writes = new Set<string>();
  for (const match of (await Bun.file(SURFACE).text()).matchAll(
    /HandleFunc\("(GET|POST) (\/api\/[^"]+)"/g,
  )) {
    (match[1] === "GET" ? reads : writes).add(match[2] ?? "");
  }
  return { reads, writes };
}

/** fixtureOf is the file a route is served from: its path under the api, as directories. */
const fixtureOf = (route: string): string =>
  join(FIXTURES, `${route.slice("/api/".length)}.json`);

describe("the fixtures", () => {
  test("answer every route the app reads", async () => {
    const { reads } = await apiRoutes();
    const called = await apiCalls();
    const read = [...called].filter((route) => reads.has(route));
    // A scan that found nothing would pass the check below it.
    expect(read.length).toBeGreaterThan(0);
    const missing: string[] = [];
    for (const route of read) {
      if (!(await Bun.file(fixtureOf(route)).exists())) {
        missing.push(route);
      }
    }
    expect(missing).toEqual([]);
  });

  test("serve every route the app names, so a typo is not mistaken for a fixture gap", async () => {
    const { reads, writes } = await apiRoutes();
    const called = await apiCalls();
    expect(called.size).toBeGreaterThan(0);
    expect([...called].filter((route) => !reads.has(route) && !writes.has(route))).toEqual([]);
  });

  test("are read by the app, so none of them is dead data", async () => {
    const { reads } = await apiRoutes();
    const read = [...(await apiCalls())].filter((route) => reads.has(route));
    const entries = await readdir(FIXTURES, { withFileTypes: true, recursive: true });
    const orphaned = entries
      .filter((entry) => entry.isFile())
      .map((entry) => join(entry.parentPath, entry.name))
      .filter((path) => !read.some((route) => fixtureOf(route) === path));
    expect(orphaned).toEqual([]);
  });

  test("hold an inbox the app's own guard accepts", async () => {
    const body: unknown = await Bun.file(join(FIXTURES, "inbox.json")).json();
    expect(isInbox(body)).toBe(true);
  });

  test("hold an autonomy view, and rewrite figures, the app's own guards accept", async () => {
    const body = (await Bun.file(join(FIXTURES, "autonomy.json")).json()) as {
      rewrite: unknown;
    };
    expect(isAutonomy(body)).toBe(true);
    expect(isRewrite(body.rewrite)).toBe(true);
  });

  test("hold a configuration, peak mode and runs-per-repo the app's guards accept", async () => {
    expect(isConfigView(await Bun.file(join(FIXTURES, "config.json")).json())).toBe(true);
    expect(isPeakView(await Bun.file(join(FIXTURES, "peak.json")).json())).toBe(true);
    expect(
      isRunsPerRepoView(await Bun.file(join(FIXTURES, "runs-per-repo.json")).json()),
    ).toBe(true);
  });

  test("name the run and the projects the pages read, in the shape they read them", async () => {
    const inbox = (await Bun.file(join(FIXTURES, "inbox.json")).json()) as {
      runs: { run: string; wait_reason: string; stopped?: boolean; stop_reason?: string }[];
    };
    expect(inbox.runs[0]?.run).toMatch(/^run_/);
    // A reason the app cannot word renders a generic row instead of the one it means.
    const unknown = inbox.runs
      .map(({ wait_reason }) => wait_reason)
      .filter((reason) => reason !== "" && !(reason in waits));
    expect(unknown).toEqual([]);
    for (const run of inbox.runs.filter(({ stop_reason }) => stop_reason !== undefined)) {
      expect(run.stopped).toBe(true);
    }

    const projects = (await Bun.file(join(FIXTURES, "projects.json")).json()) as {
      total: number;
      projects: {
        repository: string;
        reach: { status: string };
        queue: { in_flight: number; held: number };
        snapshot: { phases: unknown[] } | null;
      }[];
    };
    expect(projects.projects).toHaveLength(projects.total);
    for (const project of projects.projects) {
      expect(project.repository).toContain("/");
      expect(["reachable", "cannot-read", "checking"]).toContain(project.reach.status);
      expect(typeof project.queue.in_flight).toBe("number");
      expect(typeof project.queue.held).toBe("number");
      if (project.snapshot !== null) {
        expect(Array.isArray(project.snapshot.phases)).toBe(true);
      }
    }
  });
});
