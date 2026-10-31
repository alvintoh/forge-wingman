import { Fragment, useEffect, useId, useState } from "react";
import { wholePercent } from "../autonomy/format";
import { captionClass } from "../caption";
import { usePageSize } from "../page-size";
import { pageWindow } from "../page-window";
import { Pager } from "../pager";
import { figuresOf, hasSplit, type CachedPhase, type RunsState } from "./comparison";

const DEFINITION =
  "First request is the phase's opening call; whole phase is every call in it; whole run is every call in the run.";

const PHASE_COLUMNS: readonly CachedPhase[] = ["build", "plan"];

type CacheTableProps = {
  state: RunsState;
  // The project the page is on; the cache reads every project, and a change to
  // the filter starts the tables again at their first page.
  project: string | null;
};

/**
 * CacheTable lists each run's cached share per phase, from the endpoint's own
 * figures: its first request and its whole phase. Runs the endpoint recorded no
 * split for collapse to one counted line that opens their whole-run shares in a
 * paged table of their own. Both tables page ten a page, five on a phone.
 */
export function CacheTable({ state, project }: CacheTableProps) {
  const titleId = useId();
  const olderId = useId();
  const pageSize = usePageSize();
  const [page, setPage] = useState(0);
  const [olderPage, setOlderPage] = useState(0);
  const [olderOpen, setOlderOpen] = useState(false);
  useEffect(() => {
    setPage(0);
    setOlderPage(0);
  }, [project]);

  const runs = state.status === "ready" ? state.runs : [];
  const splitRuns = runs.filter(hasSplit);
  const olderRuns = runs.filter((run) => !hasSplit(run));
  const range = pageWindow(splitRuns.length, page, pageSize);
  const olderRange = pageWindow(olderRuns.length, olderPage, pageSize);
  const shown = splitRuns.slice(range.page * pageSize, range.page * pageSize + pageSize);
  const olderShown = olderRuns.slice(
    olderRange.page * pageSize,
    olderRange.page * pageSize + pageSize,
  );

  return (
    <section aria-labelledby={titleId} aria-busy={state.status === "loading"} className="mt-2">
      <h2 id={titleId} className={`${captionClass} text-muted`}>
        cache
      </h2>
      <p className="mt-2 text-body text-ink2">
        {DEFINITION}
        {project !== null && " Runs from all projects."}
      </p>
      {state.status === "loading" && (
        <p role="status" className="mt-6">
          Reading the runs…
        </p>
      )}
      {state.status === "error" && (
        <p role="alert" className="mt-6">
          The runs could not be read.
        </p>
      )}
      {state.status === "ready" && runs.length === 0 && <p className="mt-6">No run is recorded.</p>}
      {splitRuns.length > 0 && (
        <div className="mt-6">
          {range.paged && (
            <div className="mb-2">
              <Pager
                noun="Runs"
                total={splitRuns.length}
                page={range.page}
                pageSize={pageSize}
                onPage={setPage}
              />
            </div>
          )}
          <div className="overflow-x-auto">
            <table className="w-full text-body">
              <caption className="sr-only">
                Recent runs and each phase's first-request and whole-phase cached share
              </caption>
              <thead>
                <tr className="text-left">
                  <th scope="col" rowSpan={2} className="py-2 pr-4 align-bottom">
                    Run
                  </th>
                  <th scope="col" rowSpan={2} className="py-2 pr-4 align-bottom">
                    Provider
                  </th>
                  <th scope="col" colSpan={2} className="py-2 pr-4">
                    build
                  </th>
                  <th scope="col" colSpan={2} className="py-2">
                    plan
                  </th>
                </tr>
                <tr className="border-b border-hair text-left">
                  <th scope="col" className="py-2 pr-4">
                    First request
                  </th>
                  <th scope="col" className="py-2 pr-4">
                    Whole phase
                  </th>
                  <th scope="col" className="py-2 pr-4">
                    First request
                  </th>
                  <th scope="col" className="py-2">
                    Whole phase
                  </th>
                </tr>
              </thead>
              <tbody>
                {shown.map((run) => (
                  <tr key={run.run_id} className="border-b border-hair">
                    <th scope="row" className="py-2 pr-4 text-left font-mono">
                      {run.run_id}
                    </th>
                    <td className="py-2 pr-4 font-mono">{run.provider}</td>
                    {PHASE_COLUMNS.map((phase) => {
                      const { first, whole, word } = figuresOf(run, phase);
                      if (first === null || whole === null) {
                        return (
                          <td key={phase} colSpan={2} className="py-2 pr-4">
                            {word}
                          </td>
                        );
                      }
                      return (
                        <Fragment key={phase}>
                          <td className="py-2 pr-4">{wholePercent(first)}</td>
                          <td className="py-2 pr-4">{wholePercent(whole)}</td>
                        </Fragment>
                      );
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
      {olderRuns.length > 0 && (
        <div className="mt-4">
          <p className="text-label leading-4.5 text-ink2">
            {`${olderRuns.length} older ${olderRuns.length === 1 ? "run" : "runs"} recorded no cache split; recording began with FRG-121 on 14 October.`}
          </p>
          <button
            type="button"
            aria-expanded={olderOpen}
            aria-controls={olderId}
            onClick={() => setOlderOpen((was) => !was)}
            className="flex min-h-13 w-fit items-center text-label font-semibold text-accent underline"
          >
            {olderOpen ? "Hide older runs" : "Show older runs"}
          </button>
          <div id={olderId} hidden={!olderOpen}>
            {olderRange.paged && (
              <div className="mb-2">
                <Pager
                  noun="Runs"
                  total={olderRuns.length}
                  page={olderRange.page}
                  pageSize={pageSize}
                  onPage={setOlderPage}
                />
              </div>
            )}
            <div className="overflow-x-auto">
              <table className="w-full text-body">
                <caption className="sr-only">Older runs and their whole-run cached share</caption>
                <thead>
                  <tr className="border-b border-hair text-left">
                    <th scope="col" className="py-2 pr-4">
                      Run
                    </th>
                    <th scope="col" className="py-2">
                      Whole run
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {olderShown.map((run) => (
                    <tr key={run.run_id} className="border-b border-hair">
                      <th scope="row" className="py-2 pr-4 text-left font-mono">
                        {run.run_id}
                      </th>
                      <td className="py-2">{wholePercent(run.cached_share)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        </div>
      )}
    </section>
  );
}
