import { useState } from "react";
import { reasonText } from "../autonomy/format";
import { isAutonomy } from "../autonomy/guard";
import { RewriteFigures } from "../autonomy/rewrite-section";
import { scopeOf, useScopedFigures } from "../autonomy/scoped-figures";
import { useKnownProject } from "../project-filter";
import { ProjectFilterColumn } from "../project-filter-card";
import { useProjects } from "../project-list";
import { ProjectsPanels } from "../projects";
import { useRead } from "../stats/use-read";

// Every @[51.25rem] on this page is the Overview's split width (pages/overview.tsx). In one column
// the caption's 8.75px margin puts its baseline 26px under the Data disclosure, as drawn.
const COLUMN =
  "mt-[8.75px] min-w-0 @[51.25rem]:col-start-2 @[51.25rem]:row-start-1 @[51.25rem]:mt-0";

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
          {!released && (
            <p role="status">
              {read.status === "loading"
                ? "Reading the auto-merge state…"
                : "Reading the rewrite figures…"}
            </p>
          )}
          {read.status === "error" && <p role="alert">The auto-merge state could not be read.</p>}
          {mounted && (
            <>
              <p className="text-title font-semibold text-ink">
                {pooled.auto_merge.effective ? "Auto-merge is on" : "Auto-merge is off"}
              </p>
              <p className="mt-1 text-body text-ink2">
                {`Rate over the trailing ${pooled.auto_merge.window} merged PRs: ${pooled.auto_merge.rate_value} (bar ${pooled.auto_merge.limit}).`}
              </p>
              <p className="mt-2 text-body text-ink2">{reasonText(pooled.auto_merge.reason)}</p>
              {shown.repo !== null && (
                <p className="mt-2 text-label font-semibold text-ink">gate: all projects</p>
              )}
            </>
          )}
        </div>
        {mounted && (
          <div className="mt-10 min-w-0 @[51.25rem]:col-span-2 @[51.25rem]:row-start-2">
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
