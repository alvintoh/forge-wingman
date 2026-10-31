import { afterAll, describe, expect, test } from "bun:test";
import { mkdtempSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { findChromium } from "./chromium";
import { run } from "./main";
import { quietly } from "./quietly";

// run() end to end over a synthetic build, so the exit code and the failure
// lines it reports are proven against a real browser. Skipped when the machine
// has no Chromium.
const executable = findChromium(null);
const cleanup: string[] = [];

afterAll(() => {
  for (const dir of cleanup) {
    rmSync(dir, { recursive: true, force: true });
  }
});

function tempDir(): string {
  const dir = mkdtempSync(join(tmpdir(), "shots-main-e2e-"));
  cleanup.push(dir);
  return dir;
}

function buildOf(body: string): string {
  const dir = tempDir();
  writeFileSync(
    join(dir, "index.html"),
    `<!doctype html><meta name="viewport" content="width=device-width, initial-scale=1.0"><body>${body}</body>`,
  );
  return dir;
}

async function shoot(body: string): Promise<{ code: number; lines: string[]; out: string }> {
  const out = tempDir();
  const result = await quietly(() =>
    run([
      "--out",
      out,
      "--dist",
      buildOf(body),
      "--fixtures",
      tempDir(),
      "--routes",
      "/",
      "--chromium",
      executable!,
    ]),
  );
  return { ...result, out };
}

describe.skipIf(executable === null)("run end to end", () => {
  test("writes every shot and exits 0 for a page with nothing wrong", async () => {
    const { code, lines, out } = await shoot("<p>fine</p>");
    expect(code).toBe(0);
    expect(readdirSync(out).sort()).toEqual([
      "root-l.png",
      "root-m.png",
      "root-s.png",
      "root-xl.png",
    ]);
    expect(lines.filter((line) => line.startsWith("shotsDone count=4 "))).toHaveLength(1);
  }, 60_000);

  test("exits 1 and names each kind of failure", async () => {
    const { code, lines } = await shoot(`
      <div style="width: 2000px; height: 20px"></div>
      <script>
        fetch("/api/absent").then((answer) => {
          if (!answer.ok) console.log("answered", answer.status);
        });
        fetch("/api/save", { method: "POST", body: "{}" }).then(() => {});
        console.error("pageBroke");
      </script>`);
    expect(code).toBe(1);
    expect(lines).toContain("shotsFailed reason=missing-fixture route=/api/absent count=4");
    expect(lines).toContain('shotsFailed reason=unexpected-write request="POST /api/save" count=4');
    expect(lines.some((line) => line.startsWith("shotsFailed reason=refused shot=root-s "))).toBe(
      true,
    );
    expect(lines.some((line) => line.startsWith("shotsFailed reason=page-error route=/ "))).toBe(
      true,
    );
    expect(lines.some((line) => line.startsWith("shotsDone"))).toBe(false);
  }, 60_000);
});
