// The fixture API: one synthetic JSON file per /api route the app reads. A read
// the files do not answer is recorded, so the tool can exit non-zero rather than
// shoot a page whose data was missing.

import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

/**
 * fixtureFile maps a request path to the file that answers it: "/api/inbox" is
 * "inbox.json", "/api/projects/options" is "projects-options.json". A query
 * string is the caller's to strip; a path outside /api is not a fixture.
 */
export function fixtureFile(pathname: string): string | null {
  if (!pathname.startsWith("/api/")) {
    return null;
  }
  const rest = pathname.slice("/api/".length).replace(/\/+$/, "");
  if (rest === "") {
    return null;
  }
  return `${rest.split("/").join("-")}.json`;
}

/** The fixtures read from one directory, and the reads they could not answer. */
export class FixtureApi {
  readonly #files = new Map<string, string>();
  // The paths the app read that no fixture answered, with how often each was read.
  readonly missing = new Map<string, number>();

  constructor(dir: string) {
    for (const name of readdirSync(dir)) {
      if (!name.endsWith(".json")) {
        continue;
      }
      const text = readFileSync(join(dir, name), "utf8");
      try {
        JSON.parse(text);
      } catch (err) {
        throw new Error(`${join(dir, name)} is not valid JSON: ${(err as Error).message}`);
      }
      this.#files.set(name, text);
    }
  }

  /**
   * get answers a request path with the fixture's text, or records the miss and
   * answers null.
   */
  get(pathname: string): string | null {
    const name = fixtureFile(pathname);
    const body = name === null ? undefined : this.#files.get(name);
    if (body === undefined) {
      this.missing.set(pathname, (this.missing.get(pathname) ?? 0) + 1);
      return null;
    }
    return body;
  }
}
