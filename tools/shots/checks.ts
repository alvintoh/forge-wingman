// The quoted-string check: each non-negated criterion's strings against the text
// the shots captured, at the routes and widths the criterion names.

import type { Viewport } from "./capture";
import type { Criterion } from "./criteria";
import type { MissingString } from "./report";
import type { Route } from "./router";

/** The text one route held at one width when it was captured. */
export type PageText = {
  route: string;
  width: Viewport["key"];
  text: string;
};

/** A string no check could be made for, and why. */
type Unchecked = {
  item: number;
  string: string;
  reason: "negated" | "unreachable-state" | "route-not-shot" | "not-captured" | "empty-string";
};

/** What the string check found: how many it checked, the ones missing and the ones it could not check. */
export type StringChecks = {
  checked: number;
  missing: MissingString[];
  unchecked: Unchecked[];
};

/**
 * checkStrings looks for every string of every criterion that can be checked on
 * the pages in its scope: the routes it names that were shot, else every page,
 * narrowed to the widths it names. A string is found when any page in scope
 * shows it.
 */
export function checkStrings(
  criteria: readonly Criterion[],
  pages: readonly PageText[],
  routes: readonly Route[],
): StringChecks {
  const result: StringChecks = { checked: 0, missing: [], unchecked: [] };
  for (const criterion of criteria) {
    const skip = (reason: Unchecked["reason"]) => {
      for (const string of criterion.strings) {
        result.unchecked.push({ item: criterion.item, string, reason });
      }
    };
    if (criterion.negated) {
      skip("negated");
      continue;
    }
    if (criterion.unreachable) {
      skip("unreachable-state");
      continue;
    }
    const named = criterion.routes.filter((path) => routes.some((r) => r.pattern === path));
    const shot = named.filter((path) => pages.some((page) => page.route === path));
    if (named.length > 0 && shot.length === 0) {
      skip("route-not-shot");
      continue;
    }
    const inScope = pages.filter(
      (page) =>
        (named.length === 0 || shot.includes(page.route)) &&
        (criterion.widths.length === 0 || criterion.widths.includes(page.width)),
    );
    if (inScope.length === 0) {
      skip("not-captured");
      continue;
    }
    const state = `${shot.length > 0 ? shot.join("|") : "*"}@${
      criterion.widths.length > 0 ? criterion.widths.join("|") : "*"
    }`;
    const texts = inScope.map((page) => fold(page.text));
    for (const string of criterion.strings) {
      const wanted = fold(string).replace(/[.…]+$/, "");
      if (wanted === "") {
        result.unchecked.push({ item: criterion.item, string, reason: "empty-string" });
        continue;
      }
      result.checked++;
      if (!texts.some((text) => text.includes(wanted))) {
        result.missing.push({ state, string });
      }
    }
  }
  return result;
}

// fold makes two renderings of one sentence equal: curly quotes straight, runs
// of whitespace one space, case ignored.
function fold(text: string): string {
  return text
    .replace(/[‘’]/g, "'")
    .replace(/[“”]/g, '"')
    .replace(/\s+/g, " ")
    .trim()
    .toLowerCase();
}
