import { useId, useState } from "react";
import { wholePercent } from "../autonomy/format";
import { captionClass } from "../caption";
import { usePageSize } from "../page-size";
import { pageWindow } from "../page-window";
import { Pager } from "../pager";
import {
  columnChars,
  finishedOf,
  phaseCell,
  type ColumnChars,
  type CachedPhase,
  type PhaseCell,
  type RunCache,
  type RunsState,
} from "./comparison";

const DEFINITION =
  "Build and plan are the share of that phase's opening call that came from cache, - where it was not recorded, none where the run had no such stage; whole run is every call in the run.";

const FINISHED_HEAD = "Finished (AEST/AEDT)";

const PHASES: readonly { phase: CachedPhase; head: string }[] = [
  { phase: "build", head: "Build" },
  { phase: "plan", head: "Plan" },
];

type CacheTableProps = {
  state: RunsState;
  // The project the page is on; the cache reads every project.
  project: string | null;
};

// PhaseValue draws a share in ink, or a dim word named in full, as a bare "-" says nothing aloud.
function PhaseValue({ cell, phase }: { cell: PhaseCell; phase: CachedPhase }) {
  if (cell.kind === "share") {
    return <span className="font-mono text-ink">{wholePercent(cell.share)}</span>;
  }
  const none = cell.kind === "none";
  return (
    <span role="img" aria-label={none ? `no ${phase} stage` : "not recorded"} className="text-ink2">
      {none ? "none" : "-"}
    </span>
  );
}

// Finished is a run's settle time on Sydney's clock, on one line, or "not recorded".
function Finished({ run }: { run: RunCache }) {
  const text = finishedOf(run);
  return text === null ? (
    <span className="whitespace-nowrap text-ink2">not recorded</span>
  ) : (
    <time dateTime={run.settled_at} className="font-mono whitespace-nowrap text-ink2">
      {text}
    </time>
  );
}

const HEAD_CELL = "pr-4 font-semibold tracking-[0.3px] last:pr-0 whitespace-nowrap";
const CELL = "pr-4 whitespace-nowrap last:pr-0";

const GAP = "1rem";

// RunsTable is the runs as one table, one line a row, for a card wide enough to hold it.
function RunsTable({ runs, chars }: { runs: RunCache[]; chars: ColumnChars }) {
  return (
    <table className="hidden w-full text-label cache-wide:table">
      <caption className="sr-only">Recent runs and their cached share per phase</caption>
      <colgroup className="font-mono">
        <col style={{ width: `calc(${chars.runId}ch + ${GAP})` }} />
        <col style={{ width: `calc(${chars.provider}ch + ${GAP})` }} />
        <col style={{ width: `calc(${chars.finished}ch + ${GAP})` }} />
        <col style={{ width: `calc(4ch + ${GAP})` }} />
        <col style={{ width: `calc(4ch + ${GAP})` }} />
        <col style={{ width: "4ch" }} />
      </colgroup>
      <thead>
        <tr className="h-8.5 border-b border-hair text-left text-cap text-ink2">
          <th scope="col" className={HEAD_CELL}>
            Run
          </th>
          <th scope="col" className={HEAD_CELL}>
            Provider
          </th>
          <th scope="col" className={HEAD_CELL}>
            {FINISHED_HEAD}
          </th>
          {PHASES.map(({ phase, head }) => (
            <th key={phase} scope="col" className={`${HEAD_CELL} text-right`}>
              {head}
            </th>
          ))}
          <th scope="col" className={`${HEAD_CELL} text-right`}>
            Whole run
          </th>
        </tr>
      </thead>
      <tbody>
        {runs.map((run) => (
          <tr key={run.run_id} className="h-10 border-b border-hair">
            <th scope="row" className={`${CELL} text-left font-mono font-semibold text-ink`}>
              {run.run_id}
            </th>
            <td className={`${CELL} font-mono text-ink2`}>{run.provider}</td>
            <td className={CELL}>
              <Finished run={run} />
            </td>
            {PHASES.map(({ phase }) => (
              <td key={phase} className={`${CELL} text-right`}>
                <PhaseValue cell={phaseCell(run, phase)} phase={phase} />
              </td>
            ))}
            <td className={`${CELL} text-right font-mono text-ink`}>
              {wholePercent(run.cached_share)}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

const PAIR = "flex min-h-6 flex-wrap items-baseline justify-between gap-x-3 text-cap";
const PAIR_LABEL = "font-semibold tracking-[0.3px] text-ink2 leading-5.5";
const PAIR_VALUE = "leading-4.5";

// RunsBlocks is the runs as stacked blocks for a narrow card.
function RunsBlocks({ runs }: { runs: RunCache[] }) {
  return (
    <ul className="cache-wide:hidden">
      {runs.map((run) => (
        <li key={run.run_id} className="border-b border-hair py-2">
          <div className="flex flex-wrap items-baseline justify-between gap-x-3 leading-6">
            <span className="font-mono text-label font-semibold text-ink">{run.run_id}</span>
            <span className="min-w-0 font-mono text-cap wrap-anywhere text-ink2">
              {run.provider}
            </span>
          </div>
          <dl>
            <div className={PAIR}>
              <dt className={PAIR_LABEL}>{FINISHED_HEAD}</dt>
              <dd className={PAIR_VALUE}>
                <Finished run={run} />
              </dd>
            </div>
            {PHASES.map(({ phase, head }) => (
              <div key={phase} className={PAIR}>
                <dt className={PAIR_LABEL}>{head}</dt>
                <dd className={PAIR_VALUE}>
                  <PhaseValue cell={phaseCell(run, phase)} phase={phase} />
                </dd>
              </div>
            ))}
            <div className={PAIR}>
              <dt className={PAIR_LABEL}>Whole run</dt>
              <dd className={`${PAIR_VALUE} font-mono text-ink`}>
                {wholePercent(run.cached_share)}
              </dd>
            </div>
          </dl>
        </li>
      ))}
    </ul>
  );
}

// PagedRuns is the runs' one pager and the page of rows under it.
function PagedRuns({ runs }: { runs: RunCache[] }) {
  const pageSize = usePageSize();
  const [page, setPage] = useState(0);
  const range = pageWindow(runs.length, page, pageSize);
  const shown = runs.slice(range.page * pageSize, range.page * pageSize + pageSize);
  const chars = columnChars(runs);
  return (
    <div className="mt-5">
      {range.paged && (
        <div className="mb-3">
          <Pager
            noun="Runs"
            total={runs.length}
            page={range.page}
            pageSize={pageSize}
            onPage={setPage}
          />
        </div>
      )}
      <RunsTable runs={shown} chars={chars} />
      <RunsBlocks runs={shown} />
    </div>
  );
}

/**
 * CacheTable lists each run's cached share: its build and plan first requests and its whole run,
 * newest first, in one paged table, or in stacked blocks where the card is too narrow for one line.
 */
export function CacheTable({ state, project }: CacheTableProps) {
  const titleId = useId();
  const runs = state.status === "ready" ? state.runs : [];
  return (
    <section
      aria-labelledby={titleId}
      aria-busy={state.status === "loading"}
      className="@container mt-2"
    >
      <h2 id={titleId} className={`${captionClass} text-muted`}>
        cache
      </h2>
      <p className="mt-2 text-cap leading-4 text-ink2">{DEFINITION}</p>
      {project !== null && (
        <p className="mt-1 text-cap leading-4 text-ink2">Runs from all projects.</p>
      )}
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
      {/* The key mounts the pager again at page one on any change of filter, a return included. */}
      {runs.length > 0 && <PagedRuns key={project ?? ""} runs={runs} />}
    </section>
  );
}
