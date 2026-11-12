import { describe, expect, test } from "bun:test";
import { checkStrings, type PageText } from "./checks";
import { parseCriteria } from "./criteria";
import type { Route } from "./router";

const ROUTES: Route[] = [
  { pattern: "/", page: null, url: "/", name: "root" },
  { pattern: "/stats", page: null, url: "/stats", name: "stats" },
  { pattern: "/config", page: null, url: "/config", name: "config" },
];

function page(route: string, width: PageText["width"], text: string): PageText {
  return { route, width, text };
}

function check(items: string, pages: PageText[]) {
  return checkStrings(parseCriteria(`## Acceptance criteria\n\n${items}\n`), pages, ROUTES);
}

describe("checkStrings", () => {
  test("passes a string a page shows and counts it", () => {
    expect(check('1. shows "Cache"', [page("/", "l", "Statistics Cache")])).toEqual({
      checked: 1,
      missing: [],
      unchecked: [],
    });
  });

  test("fails a string no page shows, naming the state it was looked for at", () => {
    const result = check('1. `/stats` shows "Cache" on the phone', [
      page("/stats", "s", "Statistics"),
      page("/stats", "xs", "Statistics"),
      page("/stats", "xl", "Statistics Cache"),
    ]);
    expect(result.missing).toEqual([{ state: "/stats@xs|s", string: "Cache" }]);
  });

  test("names an unscoped string's state with a star for each part left open", () => {
    expect(check('1. shows "Cache"', [page("/", "l", "Statistics")]).missing).toEqual([
      { state: "*@*", string: "Cache" },
    ]);
  });

  test("finds a string on any page when its item names no route", () => {
    const pages = [page("/", "l", "Inbox"), page("/config", "l", "Cache")];
    expect(check('1. shows "Cache"', pages).missing).toEqual([]);
  });

  test("looks only at the route its item names", () => {
    const pages = [page("/", "l", "Cache"), page("/stats", "l", "Statistics")];
    expect(check('1. `/stats` shows "Cache"', pages).missing).toEqual([
      { state: "/stats@*", string: "Cache" },
    ]);
  });

  test("finds a string on any of the routes its item names", () => {
    const pages = [page("/stats", "l", "Statistics"), page("/config", "l", "Cache")];
    expect(check('1. `/stats` and `/config` show "Cache"', pages).missing).toEqual([]);
  });

  test("looks only at the widths its item names", () => {
    const pages = [page("/", "s", "Statistics"), page("/", "xl", "Cache")];
    expect(check('1. shows "Cache" at 440', pages).missing).toEqual([
      { state: "*@s", string: "Cache" },
    ]);
  });

  test("ignores case, runs of whitespace, curly quotes and a trailing period", () => {
    const pages = [page("/", "l", 'RUNS FROM\n  ALL   PROJECTS - it\'s "here"')];
    expect(check('1. shows “Runs from all projects.” and “it’s "here"”', pages).missing).toEqual(
      [],
    );
  });

  test("finds a string with curly quotes inside it on a page that shows straight ones", () => {
    const pages = [page("/", "l", 'Say "hi" now')];
    expect(check('1. shows "Say “hi” now"', pages).missing).toEqual([]);
  });

  test("reports a route that was not shot as unchecked, never missing", () => {
    expect(check('1. `/config` shows "Cache"', [page("/", "l", "Inbox")])).toEqual({
      checked: 0,
      missing: [],
      unchecked: [{ item: 1, string: "Cache", reason: "route-not-shot" }],
    });
  });

  test("reads a path the router does not define as no route at all", () => {
    expect(check('1. `/api/runs` shows "Inbox"', [page("/", "l", "Inbox")]).missing).toEqual([]);
  });

  test("reports a width no page was captured at as unchecked", () => {
    const result = check('1. shows "Cache" at 1280', [page("/", "s", "Cache")]);
    expect(result.unchecked).toEqual([{ item: 1, string: "Cache", reason: "not-captured" }]);
    expect(result.missing).toEqual([]);
  });

  test("skips a negated item and says so", () => {
    const result = check('1. no longer shows "Runs from all projects."', [
      page("/", "l", "Runs from all projects."),
    ]);
    expect(result).toEqual({
      checked: 0,
      missing: [],
      unchecked: [{ item: 1, string: "Runs from all projects.", reason: "negated" }],
    });
  });

  test("reports a state the run cannot reach as unchecked", () => {
    const result = check('1. with a project chosen, shows "Cache"', [page("/", "l", "Inbox")]);
    expect(result.unchecked).toEqual([{ item: 1, string: "Cache", reason: "unreachable-state" }]);
  });

  test("checks every string of an item and every item", () => {
    const result = check('1. shows "A" and "B"\n2. shows "C"', [page("/", "l", "a b")]);
    expect(result.checked).toBe(3);
    expect(result.missing).toEqual([{ state: "*@*", string: "C" }]);
  });

  test("reports a string that is only a trailing mark as unchecked, never passed", () => {
    const result = check('1. shows "..." and "…" and "Inbox"', [page("/", "l", "Inbox")]);
    expect(result.checked).toBe(1);
    expect(result.missing).toEqual([]);
    expect(result.unchecked).toEqual([
      { item: 1, string: "...", reason: "empty-string" },
      { item: 1, string: "…", reason: "empty-string" },
    ]);
  });

  test("checks nothing for an item with no quoted string", () => {
    expect(check("1. looks right", [page("/", "l", "Inbox")])).toEqual({
      checked: 0,
      missing: [],
      unchecked: [],
    });
  });
});
