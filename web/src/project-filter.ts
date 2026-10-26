import { useNavigate, useSearch } from "@tanstack/react-router";
import { useCallback, useEffect } from "react";
import type { Project } from "./project-list";
import { sameRepository, validateProjectSearch } from "./project-search";

/**
 * useProjectFilter reads the project the page is narrowed to from the URL, so a reload or a shared
 * link keeps it; `choose` sets it, or clears it with null, without adding a history entry or moving
 * the page.
 */
export function useProjectFilter() {
  const search = useSearch({ strict: false });
  // A route that does not validate its search hands over whatever the URL held.
  const project = validateProjectSearch(search).project ?? null;
  const navigate = useNavigate();
  const choose = useCallback(
    (repository: string | null) => {
      void navigate({
        to: ".",
        search: repository === null ? {} : { project: repository },
        replace: true,
        resetScroll: false,
      });
    },
    [navigate],
  );
  return { project, choose };
}

/**
 * useKnownProject finds the listed project the URL names, whatever case it is spelt in, and cleans
 * the URL once the list has settled: a project the list does not hold falls back to All, and one
 * spelt in another case takes the spelling the list serves.
 */
export function useKnownProject(projects: readonly Project[], settled: boolean) {
  const { project, choose } = useProjectFilter();
  const known =
    project === null
      ? undefined
      : projects.find((candidate) => sameRepository(candidate.repository, project));
  // An empty or non-string value has no project here: the route's validateSearch drops it and the
  // router rewrites the URL without it, so only a named project needs correcting.
  const corrected =
    settled && project !== null && known?.repository !== project
      ? (known?.repository ?? null)
      : undefined;
  useEffect(() => {
    if (corrected !== undefined) {
      choose(corrected);
    }
  }, [corrected, choose]);
  return { known, choose };
}
