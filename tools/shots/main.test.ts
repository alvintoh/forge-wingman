import { afterAll, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { run } from "./main";

const cleanup: string[] = [];

afterAll(() => {
  for (const dir of cleanup) {
    rmSync(dir, { recursive: true, force: true });
  }
});

function tempDir(): string {
  const dir = mkdtempSync(join(tmpdir(), "shots-main-"));
  cleanup.push(dir);
  return dir;
}

function dist(withIndex: boolean): string {
  const dir = tempDir();
  if (withIndex) {
    writeFileSync(join(dir, "index.html"), "<div></div>");
  }
  return dir;
}

// quietly answers the run's exit code and the lines it printed, so a passing
// test does not fill the reporter with usage text.
async function quietly(work: () => Promise<number>): Promise<{ code: number; lines: string[] }> {
  const log = console.log;
  const error = console.error;
  const lines: string[] = [];
  console.log = (...parts: unknown[]) => lines.push(parts.join(" "));
  console.error = (...parts: unknown[]) => lines.push(parts.join(" "));
  try {
    return { code: await work(), lines };
  } finally {
    console.log = log;
    console.error = error;
  }
}

describe("run", () => {
  test("prints the usage and passes for --help", async () => {
    const { code, lines } = await quietly(() => run(["--help"]));
    expect(code).toBe(0);
    expect(lines.join("\n")).toContain("usage: bun run shots");
  });

  test("fails without --out", async () => {
    const { code, lines } = await quietly(() => run([]));
    expect(code).toBe(1);
    expect(lines.join("\n")).toContain("--out is required");
  });

  test("fails when --dist is not a directory", async () => {
    const out = tempDir();
    const { code, lines } = await quietly(() =>
      run(["--out", out, "--dist", join(out, "missing")]),
    );
    expect(code).toBe(1);
    expect(lines.join("\n")).toContain("is not a directory");
  });

  test("fails when the build has no index.html", async () => {
    const out = tempDir();
    const { code, lines } = await quietly(() =>
      run(["--out", out, "--dist", dist(false), "--routes", "/"]),
    );
    expect(code).toBe(1);
    expect(lines.join("\n")).toContain("has no index.html");
  });

  test("fails when the named Chromium does not exist", async () => {
    const out = tempDir();
    const { code, lines } = await quietly(() =>
      run([
        "--out",
        out,
        "--dist",
        dist(true),
        "--routes",
        "/",
        "--chromium",
        join(out, "no-chrome"),
      ]),
    );
    expect(code).toBe(1);
    expect(lines.join("\n")).toContain("does not exist");
  });
});
