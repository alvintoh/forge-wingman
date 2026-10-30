import { describe, expect, test } from "bun:test";
import { readdir } from "node:fs/promises";
import { join } from "node:path";
import { READ_FAILURE } from "./read-failure";

const SRC = join(import.meta.dir, "../../web/src");

async function sources(): Promise<string[]> {
  const entries = await readdir(SRC, { withFileTypes: true, recursive: true });
  return entries
    .filter((entry) => entry.isFile() && /\.tsx?$/.test(entry.name))
    .map((entry) => join(entry.parentPath, entry.name));
}

describe("the read-failure tokens", () => {
  test("are the names the app gives a read that failed", async () => {
    const logged = new Set<string>();
    for (const file of await sources()) {
      const text = await Bun.file(file).text();
      // Any name, not only one in a literal: a token can be held and passed on.
      for (const match of text.matchAll(/\b[A-Za-z]+NotRead\b/g)) {
        logged.add(match[0]);
      }
    }
    // A scan that found nothing would pass the check below it.
    expect(logged.size).toBeGreaterThan(0);
    expect([...logged].filter((token) => !READ_FAILURE.test(token))).toEqual([]);
  });

  test("are refused, and a console error that is not one is left alone", () => {
    expect(READ_FAILURE.test("inboxNotRead Error: the inbox endpoint answered 500")).toBe(true);
    expect(READ_FAILURE.test("rewriteNotRead Error: bad shape")).toBe(true);
    expect(READ_FAILURE.test("signOutNotCleared Error: no redirect")).toBe(false);
    expect(READ_FAILURE.test("The rewrite rate could not be read.")).toBe(false);
  });
});
