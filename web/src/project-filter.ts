import { useNavigate, useSearch } from "@tanstack/react-router";
import { useCallback } from "react";

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

/**
 * useProjectFilter reads the project the Overview is narrowed to from the URL, so a reload or a
 * shared link keeps it; `choose` sets it, or clears it with null, without adding a history entry.
 */
export function useProjectFilter() {
  const { project } = useSearch({ from: "/" });
  const navigate = useNavigate({ from: "/" });
  const choose = useCallback(
    (repository: string | null) => {
      void navigate({ search: repository === null ? {} : { project: repository }, replace: true });
    },
    [navigate],
  );
  return { project: project ?? null, choose };
}
