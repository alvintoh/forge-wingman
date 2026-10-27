import { useState } from "react";
import { displayName, type Project } from "../project-list";
import { sameRepository } from "../project-search";
import { useRead } from "../stats/use-read";
import { isAutonomy, isRewrite } from "./guard";
import type { AutonomyView, RewriteScope } from "./types";

// The figures on screen, and the repository they are for, or null for all of them.
type Shown = { repo: string | null; figures: unknown };

// useScopedFigures gives the rewrite figures of repo, or the pooled ones of the page's own read for
// null, and keeps the figures it shows until the next ones arrive. shown is null until there are
// some; busy is whether another repository's are still being read.
export function useScopedFigures(repo: string | null, pooled: AutonomyView | null) {
  const url = repo === null ? null : `/api/autonomy?${new URLSearchParams({ repo }).toString()}`;
  const read = useRead(url, isAutonomy, "autonomyScopedNotRead");
  const [shown, setShown] = useState<Shown | null>(null);

  let next: Shown | null = null;
  if (repo === null) {
    next = pooled === null ? null : { repo, figures: pooled.rewrite };
  } else if (read.status === "ready" && read.url === url) {
    next = { repo, figures: read.body.rewrite };
  } else if (read.status === "error" && read.url === url) {
    next = { repo, figures: null };
  }
  if (
    next !== null &&
    (shown === null || next.repo !== shown.repo || next.figures !== shown.figures)
  ) {
    setShown(next);
  }

  return { shown, busy: shown !== null && shown.repo !== repo };
}

// scopeOf says how much of the measured data repo is: its PRs against every row's in the pooled
// figures, the unattributed ones included.
export function scopeOf(
  repo: string | null,
  pooled: unknown,
  projects: readonly Project[],
): RewriteScope | null {
  if (repo === null) {
    return null;
  }
  const name = displayName(repo, projects);
  if (!isRewrite(pooled)) {
    return { name, measured: 0, total: 0 };
  }
  const own = pooled.by_repo.find((row) => sameRepository(row.repo, repo));
  const total = pooled.by_repo.reduce((sum, row) => sum + row.average.prs, 0);
  return { name, measured: own?.average.prs ?? 0, total };
}
