import { afterAll, describe, expect, test } from "bun:test";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { Browser } from "playwright";
import type { CaptureOptions, CaptureResult } from "./capture";
import { BANDS } from "./bands";
import { changedPages, type Env, firstRunId, runShots } from "./run";

const work = await mkdtemp(join(tmpdir(), "shots-run-"));
const dist = join(work, "dist");
const fixtures = join(work, "fixtures");
const bare = join(work, "bare");
const out = join(work, "out");

await mkdir(join(dist, "assets"), { recursive: true });
await writeFile(join(dist, "index.html"), "<!doctype html><div id=root></div>");
await mkdir(fixtures, { recursive: true });
await writeFile(
  join(fixtures, "inbox.json"),
  JSON.stringify({
    runs: [
      {
        run: "run_01jfv3k7",
        ticket: "FRG-244",
        repo: "alvintoh/forge-wingman",
        pr_url: "https://github.com/alvintoh/forge-wingman/pull/271",
        wait_reason: "checks-not-green",
        updated_at: "2026-10-30T11:24:00Z",
        attempt: 1,
        retryable: false,
      },
    ],
  }),
);
await mkdir(bare, { recursive: true });
await mkdir(out, { recursive: true });

afterAll(async () => {
  await rm(work, { recursive: true, force: true });
});

const base = ["--dist", dist, "--out", out, "--fixtures", fixtures];

/** run drives the tool with a browser that launches, unless the test scripts one. */
async function run(argv: string[], overrides: Partial<Env> = {}) {
  const logs: string[] = [];
  const errors: string[] = [];
  const requests: CaptureOptions[] = [];
  let launched = false;
  const code = await runShots(argv, {
    root: work,
    launch: async () => {
      launched = true;
      return { close: async () => {} } as unknown as Browser;
    },
    capture: async (request: CaptureOptions): Promise<CaptureResult> => {
      requests.push(request);
      return { ok: true, path: join(request.outDir, `${request.slug}-${request.band.name}.png`) };
    },
    git: () => null,
    log: (line) => logs.push(line),
    error: (line) => errors.push(line),
    ...overrides,
  });
  return { code, logs, errors, requests, launched: () => launched };
}

describe("the exit code", () => {
  test("is 0 for the usage, which runs nothing", async () => {
    const { code, logs, requests } = await run(["--help"]);
    expect(code).toBe(0);
    expect(logs[0]).toContain("bun run shots");
    expect(requests).toEqual([]);
  });

  test("is 2 for an option the tool cannot act on", async () => {
    const { code, errors, requests } = await run(["--nope"]);
    expect(code).toBe(2);
    expect(errors[0]).toContain("unknown argument --nope");
    expect(requests).toEqual([]);
  });

  test("is 1, and nothing is launched, when there is no build to serve", async () => {
    const { code, errors, launched } = await run([
      "--dist",
      bare,
      "--out",
      out,
      "--fixtures",
      fixtures,
    ]);
    expect(code).toBe(1);
    expect(errors[0]).toContain("holds no index.html");
    expect(launched()).toBe(false);
  });

  test("is 2 for a route no page is drawn at", async () => {
    const { code, errors, requests } = await run([...base, "--routes", "/nope"]);
    expect(code).toBe(2);
    expect(errors[0]).toContain("no page is routed at /nope");
    expect(requests).toEqual([]);
  });

  test("is 1 when the browser will not start", async () => {
    const { code, errors } = await run([...base, "--routes", "/stats"], {
      launch: async () => {
        throw new Error("Chromium could not start: no executable");
      },
    });
    expect(code).toBe(1);
    expect(errors[0]).toContain("no executable");
  });

  test("is 0 with every shot written, one per band", async () => {
    const { code, logs, requests } = await run([...base, "--routes", "/stats"]);
    expect(code).toBe(0);
    expect(requests.map(({ slug, band }) => `${slug}-${band.name}`)).toEqual(
      BANDS.map((band) => `stats-${band.name}`),
    );
    expect(logs.filter((line) => line.startsWith("saved "))).toHaveLength(4);
    expect(logs.at(-1)).toContain("4 shot(s)");
  });

  test("is 1 when one band's shot is refused, naming it", async () => {
    const { code, errors, logs } = await run([...base, "--routes", "/stats"], {
      capture: async (request) =>
        request.band.name === "l"
          ? { ok: false, reason: "the fonts did not finish loading" }
          : { ok: true, path: join(request.outDir, `${request.slug}-${request.band.name}.png`) },
    });
    expect(code).toBe(1);
    expect(errors).toContain("  /stats at l: the fonts did not finish loading");
    expect(logs.at(-1)).not.toContain("shot(s) in");
  });

  test("is 1 when the app read an api route the fixtures lack", async () => {
    const { code, errors } = await run([...base, "--routes", "/stats"], {
      capture: async (request) => {
        await fetch(new URL("/api/rewrite-ledger", request.url));
        return { ok: true, path: join(request.outDir, `${request.slug}-${request.band.name}.png`) };
      },
    });
    expect(code).toBe(1);
    expect(errors[0]).toContain("no fixture answers");
    expect(errors).toContain("  /api/rewrite-ledger");
  });

  test("is 1 when a route needs a run id the fixtures do not name, and the rest still run", async () => {
    const { code, errors, requests, logs } = await run([
      "--dist",
      dist,
      "--out",
      out,
      "--fixtures",
      bare,
      "--routes",
      "/runs/$runId,/stats",
    ]);
    expect(code).toBe(1);
    expect(errors).toContain(
      "shots: /runs/$runId needs a run id, and the inbox fixture lists no run to take one from",
    );
    expect(requests.map(({ slug }) => slug)).toEqual(BANDS.map(() => "stats"));
    expect(logs.filter((line) => line.startsWith("saved "))).toHaveLength(4);
  });

  test("is 0 for a parameterised route named with its own path", async () => {
    const { code, requests } = await run([...base, "--routes", "/runs/run_01jfv3k7"]);
    expect(code).toBe(0);
    expect(requests[0]?.slug).toBe("runs-run_01jfv3k7");
  });

  test("is 0 for every route when git cannot say what changed", async () => {
    const { code, requests } = await run(base, { git: () => null });
    expect(code).toBe(0);
    expect(requests).toHaveLength(6 * BANDS.length);
  });

  test("is 0 for only the route whose page file the branch added", async () => {
    const { code, requests } = await run(base, {
      git: (args) => {
        if (args[0] === "merge-base") return "base";
        if (args[0] === "ls-files") return "web/src/pages/stats.tsx";
        return "";
      },
    });
    expect(code).toBe(0);
    expect(requests.map(({ slug }) => slug)).toEqual(BANDS.map(() => "stats"));
  });
});

describe("the changed page files", () => {
  const answers: Record<string, string> = {
    "merge-base HEAD origin/main": "base",
    "diff --name-only base HEAD": "web/src/pages/stats.tsx\nweb/src/inbox/row.tsx",
    "diff --name-only HEAD": "web/src/pages/config.tsx",
    "ls-files --others --exclude-standard": "web/src/pages/autonomy.tsx\nweb/src/inbox/row.tsx",
  };
  const git = (args: readonly string[]): string | null => answers[args.join(" ")] ?? null;

  test("include a page file the branch added, which no diff names", () => {
    expect(changedPages(git, "origin/main")).toEqual([
      "web/src/pages/stats.tsx",
      "web/src/inbox/row.tsx",
      "web/src/pages/config.tsx",
      "web/src/pages/autonomy.tsx",
    ]);
  });

  test("fall back to main when the base ref cannot be read", () => {
    const seen: string[] = [];
    const fallback = (args: readonly string[]): string | null => {
      seen.push(args.join(" "));
      return args[0] === "merge-base" && args[2] === "main" ? "base" : null;
    };
    expect(changedPages(fallback, "origin/trunk")).toEqual([]);
    expect(seen).toContain("merge-base HEAD main");
  });

  test("are none when git cannot answer at all", () => {
    expect(changedPages(() => null, "origin/main")).toEqual([]);
  });
});

describe("the run id the run route is captured at", () => {
  test("is the one the inbox fixture lists first", async () => {
    expect(await firstRunId(fixtures)).toBe("run_01jfv3k7");
  });

  test("is absent when the fixtures name no run", async () => {
    expect(await firstRunId(bare)).toBeNull();
  });
});
