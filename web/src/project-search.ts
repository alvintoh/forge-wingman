type ProjectSearch = { project?: string };

/** validateProjectSearch keeps a `project` that is a non-empty string; `?project=2` is no choice. */
export function validateProjectSearch(search: Record<string, unknown>): ProjectSearch {
  const { project } = search;
  // An explicit undefined, not an omitted key: the parent route's raw `project` would show through.
  return { project: typeof project === "string" && project !== "" ? project : undefined };
}

/** sameRepository compares two `owner/name` spellings, which GitHub treats alike in any case. */
export function sameRepository(a: string, b: string): boolean {
  return a.toLowerCase() === b.toLowerCase();
}
