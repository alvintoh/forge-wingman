import { afterAll, describe, expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { baseCandidates, changedPages, parseChangedPages } from "./changed";

const cleanup: string[] = [];

afterAll(() => {
  for (const dir of cleanup) {
    rmSync(dir, { recursive: true, force: true });
  }
});

function git(root: string, ...args: string[]): void {
  const run = Bun.spawnSync(
    ["git", "-c", "user.name=shots", "-c", "user.email=shots@example.com", "-C", root, ...args],
    { stdout: "ignore", stderr: "pipe" },
  );
  if (run.exitCode !== 0) {
    throw new Error(`git ${args.join(" ")} failed: ${run.stderr.toString()}`);
  }
}

function tempRepo(): string {
  const root = mkdtempSync(join(tmpdir(), "shots-git-"));
  cleanup.push(root);
  git(root, "init", "-b", "main");
  mkdirSync(join(root, "web/src/pages"), { recursive: true });
  writeFileSync(join(root, "web/src/pages/stats.tsx"), "one\n");
  git(root, "add", ".");
  git(root, "commit", "-m", "first");
  return root;
}

describe("parseChangedPages", () => {
  test("reads one path per line, ignoring blank lines", () => {
    expect(parseChangedPages("web/src/pages/stats.tsx\n\nweb/src/pages/run.tsx\n")).toEqual(
      new Set(["web/src/pages/stats.tsx", "web/src/pages/run.tsx"]),
    );
  });
});

describe("baseCandidates", () => {
  test("offers the remote-tracking branch when the base has no slash", () => {
    expect(baseCandidates("main")).toEqual(["main", "origin/main"]);
    expect(baseCandidates("origin/main")).toEqual(["origin/main"]);
  });
});

describe("changedPages", () => {
  test("lists the files changed on the branch since the base", () => {
    const root = tempRepo();
    git(root, "checkout", "-b", "feature");
    writeFileSync(join(root, "web/src/pages/stats.tsx"), "two\n");
    writeFileSync(join(root, "web/src/pages/run.tsx"), "new\n");
    git(root, "add", ".");
    git(root, "commit", "-m", "second");

    expect(changedPages(root, "main")).toEqual(
      new Set(["web/src/pages/stats.tsx", "web/src/pages/run.tsx"]),
    );
  });

  test("names the missing base in its error", () => {
    const root = tempRepo();
    expect(() => changedPages(root, "no-such-branch")).toThrow(
      "no commit named no-such-branch or origin/no-such-branch",
    );
  });

  test("names a diff that git cannot make", () => {
    const root = tempRepo();
    // An unborn HEAD resolves the base but leaves nothing for git diff to compare.
    git(root, "checkout", "--orphan", "orphan");
    expect(() => changedPages(root, "main")).toThrow("git diff against main failed");
  });
});
