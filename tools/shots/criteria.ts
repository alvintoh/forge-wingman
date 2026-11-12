// A ticket's acceptance criteria, read the way internal/dispatcher/sizer.go reads
// them, and what each criterion asks of the screenshots.

import { readFileSync } from "node:fs";
import type { Viewport } from "./capture";

/** What one numbered or bulleted criterion asks of the shots. */
export type Criterion = {
  // The criterion's place in its section, from 1.
  item: number;
  // Every straight or curly double-quoted string, as written.
  strings: string[];
  // The route paths it names, e.g. "/stats".
  routes: string[];
  // The widths it narrows to; empty means any.
  widths: Viewport["key"][];
  // It says what must not show, which a page cannot prove.
  negated: boolean;
  // It describes a state the run cannot reach.
  unreachable: boolean;
};

const AC_HEADING =
  /^[ \t]*(#+)[ \t]*acceptance criteria\b|^[ \t]*\*\*[ \t]*acceptance criteria\b|^[ \t]*acceptance criteria[ \t]*(?:\([^)\n]*\))?[ \t]*:?[ \t]*$/i;
const MD_HEADING = /^[ \t]*(#+)[ \t]/;
const BOLD_LINE = /^[ \t]*\*\*[^*\n]+\*\*/;
const AMENDMENT = /^[ \t]*\*\*[ \t]*(?:updated|added|superseded|removed)\b/i;
const AC_ITEM = /^ {0,3}(?:\d+[.)]|[-*+])[ \t]/;

const STRUCK = /~~.*?~~/g;
const MARKDOWN_ESCAPE = /\\([\\`*_{}[\]()#+\-.!|<>~])/g;
const QUOTED = /"([^"]*)"|“([^”]*)”/g;
const NEGATION = /\b(?:no|not|never|without)\b|n['’]t\b/i;
const UNREACHABLE_STATE = /\b(?:loading|empty|chosen|selected)\b/i;
const ROUTE_PATH = /(?<![\w/.:$-])\/[a-z$][\w$-]*(?:\/[\w$-]+)*/gi;

const WIDTH_BY_NUMBER: Readonly<Record<string, Viewport["key"]>> = {
  "320": "xs",
  "440": "s",
  "768": "m",
  "1024": "l",
  "1280": "xl",
};
const WIDTH_ORDER: readonly Viewport["key"][] = ["xs", "s", "m", "l", "xl"];

/**
 * readCriteria reads a ticket file as the runner's ticket subcommand wrote it
 * and parses its description's acceptance criteria.
 */
export function readCriteria(path: string): Criterion[] {
  let body: unknown;
  try {
    body = (JSON.parse(readFileSync(path, "utf8")) as { body?: unknown }).body;
  } catch (err) {
    throw new Error(
      `--ticket ${path} is not a readable ticket: ${err instanceof Error ? err.message : String(err)}`,
    );
  }
  if (typeof body !== "string") {
    throw new Error(`--ticket ${path} has no body`);
  }
  return parseCriteria(body);
}

/**
 * parseCriteria reads the items of a description's acceptance-criteria section,
 * with struck-through text dropped, and answers none when it has no section.
 */
export function parseCriteria(body: string): Criterion[] {
  return sectionItems(body).map((raw, index) => {
    const text = raw.replace(STRUCK, "");
    const strings = [...text.matchAll(QUOTED)]
      .map((match) => (match[1] ?? match[2] ?? "").replace(MARKDOWN_ESCAPE, "$1").trim())
      .filter((quoted) => quoted !== "");
    const prose = text.replace(QUOTED, " ");
    return {
      item: index + 1,
      strings,
      routes: [...new Set(prose.match(ROUTE_PATH) ?? [])],
      widths: widthsOf(prose),
      negated: NEGATION.test(prose),
      unreachable: UNREACHABLE_STATE.test(prose),
    };
  });
}

function widthsOf(prose: string): Viewport["key"][] {
  const named = new Set<Viewport["key"]>();
  for (const number of prose.match(/\b(?:320|440|768|1024|1280)\b/g) ?? []) {
    named.add(WIDTH_BY_NUMBER[number]!);
  }
  for (const key of prose.matchAll(/\b(?:(?:at|on|in) (xs|s|m|l|xl)|(xs|xl))\b/gi)) {
    named.add((key[1] ?? key[2])!.toLowerCase() as Viewport["key"]);
  }
  if (/\b(?:phone|mobile)\b/i.test(prose)) {
    named.add("xs").add("s");
  }
  if (/\btablet\b/i.test(prose)) {
    named.add("m");
  }
  if (/\bdesktop\b/i.test(prose)) {
    named.add("l").add("xl");
  }
  return WIDTH_ORDER.filter((key) => named.has(key));
}

// sectionItems is the section's items as one line each: a heading's section runs
// to the next heading of its level or above, a bold or bare label's to the next
// heading or bold line that is not an amendment note.
function sectionItems(body: string): string[] {
  const lines = body.split("\n");
  const start = lines.findIndex((line) => AC_HEADING.test(line));
  if (start === -1) {
    return [];
  }
  const level = (AC_HEADING.exec(lines[start]!)?.[1] ?? "").length;
  let end = lines.length;
  for (let i = start + 1; i < lines.length; i++) {
    const hashes = MD_HEADING.exec(lines[i]!)?.[1];
    const closes =
      hashes !== undefined
        ? level === 0 || hashes.length <= level
        : level === 0 && BOLD_LINE.test(lines[i]!) && !AMENDMENT.test(lines[i]!);
    if (closes) {
      end = i;
      break;
    }
  }
  const items: string[] = [];
  for (const line of lines.slice(start + 1, end)) {
    if (AC_ITEM.test(line)) {
      items.push(line.replace(AC_ITEM, "").trim());
    } else if (items.length > 0) {
      items[items.length - 1] += ` ${line.trim()}`;
    }
  }
  return items;
}
