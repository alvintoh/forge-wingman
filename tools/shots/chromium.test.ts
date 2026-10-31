import { afterAll, describe, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { findChromium, launchChromium } from "./chromium";

const dir = mkdtempSync(join(tmpdir(), "shots-chromium-"));

afterAll(() => {
  rmSync(dir, { recursive: true, force: true });
});

describe("findChromium", () => {
  test("takes the given binary, else $CHROME_PATH", () => {
    const bin = join(dir, "chrome");
    writeFileSync(bin, "");
    const saved = process.env.CHROME_PATH;
    process.env.CHROME_PATH = bin;
    try {
      expect(findChromium(bin)).toBe(bin);
      expect(findChromium("/definitely/not/here")).toBe(bin);
    } finally {
      if (saved === undefined) {
        delete process.env.CHROME_PATH;
      } else {
        process.env.CHROME_PATH = saved;
      }
    }
  });
});

describe("launchChromium", () => {
  test("fails when the executable is missing", async () => {
    await expect(launchChromium(join(dir, "no-browser"))).rejects.toThrow();
  });

  test("fails when the browser exits before its debugging port opens", async () => {
    await expect(launchChromium(Bun.which("false") ?? "false")).rejects.toThrow(
      "exited before it was ready",
    );
  }, 10_000);
});
