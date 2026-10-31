import { useEffect, useState } from "react";
import { captionClass } from "../caption";
import { useKnownProject, useProjectFilter } from "../project-filter";
import { ProjectFilterColumn } from "../project-filter-card";
import { useProjects } from "../project-list";
import { CacheTable } from "../stats/cache-table";
import { isComparison, type RunsState } from "../stats/comparison";
import { RewriteRate } from "../stats/rewrite-rate";
import { RunHistory } from "../stats/run-history";

// Statistics lists the recent runs and the share of each phase's input the
// provider served from cache, with the project filter that narrows the runs;
// the rewrite-rate figures stay pooled and say so.
export function Stats() {
  const [state, setState] = useState<RunsState>({ status: "loading", runs: [] });
  const { state: list, retry } = useProjects();
  const { project } = useProjectFilter();
  const { known, choose } = useKnownProject(list.projects, list.status === "ready");
  // The strip follows the URL at once, so a project chosen before the list arrives still narrows
  // it; once the list has answered the caption names the project as the list serves it.
  const narrowed = known?.repository ?? project;
  // The cached-share block mounts with the projects column's first answer and stays, so on a phone,
  // where the block sits under the column, the card's arrival never moves it and a retry of the
  // list never takes the block away.
  const [settled, setSettled] = useState(list.status !== "loading");
  if (!settled && list.status !== "loading") {
    setSettled(true);
  }

  useEffect(() => {
    const controller = new AbortController();

    async function read() {
      const response = await fetch("/api/comparison", { signal: controller.signal });
      if (!response.ok) {
        throw new Error(`the comparison endpoint answered ${response.status}`);
      }
      const body: unknown = await response.json();
      if (!isComparison(body)) {
        throw new Error("the comparison endpoint answered no run list");
      }
      if (controller.signal.aborted) {
        return;
      }
      setState({ status: "ready", runs: body.runs });
    }

    read().catch((err: unknown) => {
      if (controller.signal.aborted) {
        return;
      }
      console.error("comparisonNotRead", err);
      setState({ status: "error", runs: [] });
    });

    return () => controller.abort();
  }, []);

  return (
    <section className="mx-auto max-w-desk px-padx pt-4 pb-band">
      {/* Two columns once the page's width holds the filter card beside a run history, at the
          Overview's 51.25rem. Side by side the page's caption and the projects caption share a row,
          level on that caption's own 3.25px inset, and each column's body starts level under it;
          narrower, the bodies stack with the filter column last. */}
      <div className="mb-8 grid grid-cols-1 @[51.25rem]:grid-cols-[minmax(0,1fr)_23rem] @[51.25rem]:grid-rows-[auto_auto] @[51.25rem]:gap-x-8">
        <h1
          className={`${captionClass} py-[3.25px] text-ink2 @[51.25rem]:col-start-1 @[51.25rem]:row-start-1`}
        >
          statistics
        </h1>
        <div className="mt-4 flex min-w-0 flex-col gap-y-6 @[51.25rem]:col-start-1 @[51.25rem]:row-start-2">
          <RunHistory project={narrowed} />
          <RewriteRate projectChosen={project !== null} />
        </div>
        {/* The filter column's caption and its card take the page's two rows, so the card's top is
            the run history's however the card grows and shrinks. */}
        <ProjectFilterColumn
          state={list}
          retry={retry}
          known={known}
          onChoose={choose}
          className="mt-6 min-w-0 @[51.25rem]:col-start-2 @[51.25rem]:row-start-1 @[51.25rem]:row-span-2 @[51.25rem]:mt-0 @[51.25rem]:grid @[51.25rem]:grid-cols-1 @[51.25rem]:grid-rows-subgrid @[51.25rem]:items-start"
        />
      </div>
      {settled && <CacheTable state={state} project={project} />}
    </section>
  );
}
