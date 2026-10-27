import { useState } from "react";
import { isAutonomy } from "../autonomy/guard";
import { RewriteFigures } from "../autonomy/rewrite-section";
import { scopeOf, useScopedFigures } from "../autonomy/scoped-figures";
import { MergeStatusCard } from "../autonomy/status-card";
import { useKnownProject } from "../project-filter";
import { ProjectFilterColumn } from "../project-filter-card";
import { useProjects } from "../project-list";
import { ProjectsPanels } from "../projects";
import { useRead } from "../stats/use-read";

// Every @[51.25rem] on this page is the Overview's split width (pages/overview.tsx). The gaps are
// the drawn ones: the project card's top is the auto-merge card's top, and the rewrite section is
// 10px under the card on a phone and 14px under the lower of the two columns on a desktop.
const COLUMN = "min-w-0 @[51.25rem]:col-start-2 @[51.25rem]:row-start-1";

// Autonomy shows whether an eligible pull request merges itself: the state over
// the trailing window, the rate behind it and the reason, then the rewrite rate
// as charts. The switch is the owner's setting, set with `runner auto-merge`. A
// project chosen in the filter narrows the rewrite rate to it; the state stays pooled.
export function Autonomy() {
  const read = useRead("/api/autonomy", isAutonomy, "autonomyNotRead");
  const { state: list, retry } = useProjects();
  const { known, choose } = useKnownProject(list.projects, list.status === "ready");
  const pooled = read.status === "ready" ? read.body : null;
  const { shown, busy } = useScopedFigures(known?.repository ?? null, pooled);

  // The body mounts once, as one unit, when every read it draws has answered, so a card arriving
  // late never pushes another down; a later re-read of the list leaves it mounted.
  const [released, setReleased] = useState(false);
  if (
    !released &&
    (read.status === "error" ||
      (pooled !== null && list.status !== "loading" && shown !== null && !busy))
  ) {
    setReleased(true);
  }
  const mounted = released && pooled !== null && shown !== null;

  return (
    <section className="mx-auto max-w-desk px-padx pt-4 pb-band">
      <h1 className="text-title font-semibold text-ink">Autonomy</h1>
      <p className="mt-2 text-body text-ink2">Whether an eligible PR merges itself.</p>
      <div className="mt-6 grid grid-cols-1 @[51.25rem]:grid-cols-[minmax(0,1fr)_23rem] @[51.25rem]:grid-rows-[auto_auto] @[51.25rem]:gap-x-8">
        <div className="min-w-0 @[51.25rem]:col-start-1 @[51.25rem]:row-start-1">
          <MergeStatusCard
            state={read.status}
            merge={pooled === null ? null : pooled.auto_merge}
            pooled={shown !== null && shown.repo !== null}
          />
        </div>
        {mounted && (
          <div className="mt-2.5 min-w-0 @[51.25rem]:col-span-2 @[51.25rem]:row-start-2 @[51.25rem]:mt-3.5">
            <RewriteFigures
              figures={shown.figures}
              scope={scopeOf(shown.repo, pooled.rewrite, list.projects)}
              busy={busy}
            />
          </div>
        )}
        {read.status !== "error" &&
          (mounted ? (
            <ProjectFilterColumn
              state={list}
              retry={retry}
              known={known}
              onChoose={choose}
              className={COLUMN}
            />
          ) : (
            <div className={COLUMN}>
              <ProjectsPanels state={list} retry={retry}>
                {null}
              </ProjectsPanels>
            </div>
          ))}
      </div>
    </section>
  );
}
