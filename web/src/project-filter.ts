import { useNavigate, useSearch } from "@tanstack/react-router";
import { useCallback } from "react";

/**
 * useProjectFilter reads the project the Overview is narrowed to from the URL, so a reload or a
 * shared link keeps it; `choose` sets it, or clears it with null, without adding a history entry or
 * moving the page.
 */
export function useProjectFilter() {
  const { project } = useSearch({ from: "/" });
  const navigate = useNavigate({ from: "/" });
  const choose = useCallback(
    (repository: string | null) => {
      void navigate({
        search: repository === null ? {} : { project: repository },
        replace: true,
        resetScroll: false,
      });
    },
    [navigate],
  );
  return { project: project ?? null, choose };
}
