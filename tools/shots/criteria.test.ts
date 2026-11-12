import { afterAll, describe, expect, test } from "bun:test";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { parseCriteria, readCriteria } from "./criteria";

const cleanup: string[] = [];

afterAll(() => {
  for (const dir of cleanup) {
    rmSync(dir, { recursive: true, force: true });
  }
});

function ticketFile(contents: string): string {
  const dir = mkdtempSync(join(tmpdir(), "shots-criteria-"));
  cleanup.push(dir);
  const path = join(dir, "ticket.json");
  writeFileSync(path, contents);
  return path;
}

// one parses a single criterion written as the only item of a section.
function one(item: string) {
  return parseCriteria(`## Acceptance criteria\n\n1. ${item}\n`)[0]!;
}

describe("parseCriteria sections", () => {
  test("reads the items of a heading's section and stops at the next heading of its level", () => {
    const items = parseCriteria(
      "Intro.\n\n## Acceptance criteria\n\n1. one\n2. two\n\n## Out of scope\n\n1. not a criterion\n",
    );
    expect(items.map((criterion) => criterion.item)).toEqual([1, 2]);
  });

  test("keeps a subheading and its items inside the section", () => {
    const items = parseCriteria(
      "## Acceptance criteria\n\n1. one\n\n### Phone\n\n2. two\n\n## Notes\n",
    );
    expect(items).toHaveLength(2);
  });

  test("reads a heading with a suffix, a bold label and a bare label", () => {
    expect(parseCriteria("## Acceptance criteria (updated)\n\n1. one\n")).toHaveLength(1);
    expect(
      parseCriteria("**Acceptance criteria**\n\n1. one\n\n**Out of scope**\n\n1. x\n"),
    ).toHaveLength(1);
    expect(parseCriteria("Acceptance criteria:\n\n- one\n- two\n")).toHaveLength(2);
  });

  test("keeps an amendment note inside a bold label's section", () => {
    const items = parseCriteria(
      "**Acceptance criteria**\n\n1. one\n\n**Updated (2026-11-05):**\n\n2. two\n",
    );
    expect(items).toHaveLength(2);
  });

  test("answers nothing for a description with no section", () => {
    expect(parseCriteria("## Problem\n\n1. not a criterion\n")).toEqual([]);
  });

  test("reads numbered and bulleted items and joins a wrapped item", () => {
    const items = parseCriteria(
      "## Acceptance criteria\n\n1. first\n   wraps here\n2) second\n- third\n* fourth\n",
    );
    expect(items).toHaveLength(4);
    expect(items.map((criterion) => criterion.item)).toEqual([1, 2, 3, 4]);
    expect(one('first\n   with "two\n   lines"').strings).toEqual(["two lines"]);
  });
});

describe("parseCriteria quoted strings", () => {
  test("takes straight and curly double-quoted strings, as written", () => {
    expect(one('shows "Runs" and “Runs from all projects.”').strings).toEqual([
      "Runs",
      "Runs from all projects.",
    ]);
  });

  test("never takes backticks or single quotes", () => {
    expect(one("shows `Runs` and 'Statistics' and ‘Cache’").strings).toEqual([]);
  });

  test("drops struck-through text and keeps the amendment after it", () => {
    expect(one('~~shows "Old"~~ *Updated (2026-11-05):* shows "New"').strings).toEqual(["New"]);
  });

  test("unescapes the markdown backslashes a tracker saves before punctuation", () => {
    expect(one('shows "snake\\_case \\*key\\*" and "a\\.b"').strings).toEqual([
      "snake_case *key*",
      "a.b",
    ]);
  });

  test("drops an empty quote", () => {
    expect(one('shows "" and " "').strings).toEqual([]);
  });
});

describe("parseCriteria routes and widths", () => {
  test("names the route paths an item writes", () => {
    expect(one("`/stats` and `/runs/$runId` show it, twice on /stats").routes).toEqual([
      "/stats",
      "/runs/$runId",
    ]);
  });

  test("does not read a slash inside a word, a URL or a quoted string as a route", () => {
    expect(one('and/or https://example.com/stats shows "/config"').routes).toEqual([]);
  });

  test("names the widths an item writes, narrowest first", () => {
    expect(one("at 1280 and 320").widths).toEqual(["xs", "xl"]);
    expect(one("on the phone").widths).toEqual(["xs", "s"]);
    expect(one("on a tablet").widths).toEqual(["m"]);
    expect(one("on desktop").widths).toEqual(["l", "xl"]);
    expect(one("at s and at xl").widths).toEqual(["s", "xl"]);
  });

  test("narrows to nothing when an item names no width", () => {
    expect(one("shows it").widths).toEqual([]);
  });
});

describe("parseCriteria cues", () => {
  test.each([
    "no longer shows it",
    "it is not shown",
    "never wraps",
    "renders without it",
    "it doesn't wrap",
  ])("flags %p as negated", (item: string) => {
    expect(one(item).negated).toBe(true);
  });

  test("reads a negation cue only in the prose, never in a quoted string", () => {
    expect(one('shows "No runs yet"').negated).toBe(false);
  });

  test("flags the states the run cannot reach", () => {
    expect(one("while loading").unreachable).toBe(true);
    expect(one("with a project chosen").unreachable).toBe(true);
    expect(one("an empty list").unreachable).toBe(true);
    expect(one("the list shows it").unreachable).toBe(false);
  });
});

describe("readCriteria", () => {
  test("reads a ticket file's description", () => {
    const body = '## Acceptance criteria\n\n1. `/stats` shows "Cache".\n';
    const [criterion] = readCriteria(ticketFile(JSON.stringify({ id: "T-1", body })));
    expect(criterion).toEqual({
      item: 1,
      strings: ["Cache"],
      routes: ["/stats"],
      widths: [],
      negated: false,
      unreachable: false,
    });
  });

  test("names the file when it is missing, is not JSON or has no body", () => {
    const missing = join(tmpdir(), "shots-criteria-absent.json");
    expect(() => readCriteria(missing)).toThrow(`--ticket ${missing} is not a readable ticket`);
    const notJSON = ticketFile("not json");
    expect(() => readCriteria(notJSON)).toThrow(`--ticket ${notJSON} is not a readable ticket`);
    const noBody = ticketFile(JSON.stringify({ id: "T-1" }));
    expect(() => readCriteria(noBody)).toThrow(`--ticket ${noBody} has no body`);
  });
});

// The tickets the dispatcher's sizer reads with the same section and item
// patterns (internal/dispatcher/sizer.go), so a pattern that drifts shows here.
describe("parseCriteria over the dispatcher's sized tickets", () => {
  const corpus: { id: string; body: string }[] = JSON.parse(
    readFileSync(
      resolve(import.meta.dir, "../../internal/dispatcher/testdata/sized_tickets.json"),
      "utf8",
    ),
  );

  test("finds the criteria of every ticket whose description has a section", () => {
    expect(corpus.length).toBeGreaterThan(0);
    const empty = corpus
      .filter((ticket) =>
        /^[ \t]*(?:#+[ \t]*|\*\*[ \t]*)?acceptance criteria\b/im.test(ticket.body),
      )
      .filter((ticket) => parseCriteria(ticket.body).length === 0)
      .map((ticket) => ticket.id);
    expect(empty).toEqual([]);
  });

  test("reads every quoted string as one non-empty line with no struck text left", () => {
    for (const ticket of corpus) {
      for (const criterion of parseCriteria(ticket.body)) {
        for (const string of criterion.strings) {
          expect(string.trim()).not.toBe("");
          expect(string).not.toContain("\n");
          expect(string).not.toContain("~~");
        }
      }
    }
  });
});
