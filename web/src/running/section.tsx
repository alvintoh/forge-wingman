import { useId, useState } from "react";
import { DisclosureRow } from "../disclosure-row";
import { TryAgainPill } from "../pill-button";
import { useProjectFilter } from "../project-filter";
import { displayName, type Project, type ProjectsState } from "../project-list";
import { sameRepository } from "../project-search";
import { RefreshButton, RefreshClock, RefreshNotice } from "../refresh-control";
import type { PageRefresh } from "../refresh-read";
import type { RunningRead } from "./read";
import {
  DEFER_FROM_PERCENT,
  type ColumnChars,
  type QueueCounts,
  type RunningRun,
  barWords,
  columnChars,
  figureWords,
  minutesOf,
  overdueBy,
  percentOf,
  queueWords,
  timeWords,
} from "./run";
import { useWrapped } from "./use-wrapped";

// The section lists this many runs before the row that opens the rest.
const SHOWN_RUNS = 3;

// countsOf sums the counts of the chosen project, or of every project. Held counts only where a
// stopped or stopping project holds its queue back; a running one's queue is about to move.
function countsOf(projects: ProjectsState, chosen: string | null): QueueCounts {
  const narrowed =
    chosen === null
      ? projects.projects
      : projects.projects.filter((project) => sameRepository(project.repository, chosen));
  let queued = 0;
  let held = 0;
  for (const project of narrowed) {
    if (project.paused) {
      held += project.queue.held;
    } else if (project.snapshot !== null && !project.snapshot.unreadable) {
      queued += project.snapshot.queued;
    }
  }
  return { queued, held };
}

type RowProps = {
  run: RunningRun;
  // The instant the list was read, so every bar measures one clock.
  now: number;
  // Whether the row names its project: under All projects, not one chosen.
  named: boolean;
  projects: readonly Project[];
  // The card's widest figure and time, so every row sizes its columns alike.
  chars: ColumnChars;
};

// RunningRow is one run in flight: its stage, ticket and project over its title, then the bar of the
// time its stage has used. The row goes nowhere: a run in flight has no PR, and the bar is time,
// never work done.
function RunningRow({ run, now, named, projects, chars }: RowProps) {
  const minutes = minutesOf(run.claimed_at, now);
  const limit = run.limit_minutes;
  const percent = percentOf(minutes, limit);
  const over = overdueBy(minutes, limit);
  const { ref: lineRef, wrapped } = useWrapped<HTMLDivElement>();
  return (
    <li className="border-t border-hair py-3 first:border-t-0">
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
        <span className="inline-flex h-5.5 shrink-0 items-center rounded-full bg-accent px-2.5 font-mono text-micro font-semibold tracking-[1.2px] text-card">
          {run.stage.toUpperCase()}
        </span>
        <span className="shrink-0 font-mono text-micro font-semibold tracking-[0.6px] text-ink2">
          {run.ticket}
        </span>
        {/* Past the line's room the project name takes a line of its own, and truncates there. */}
        {named && (
          <span className="max-w-full shrink-0 truncate font-mono text-micro tracking-[0.4px] text-muted">
            {displayName(run.repo, projects)}
          </span>
        )}
      </div>
      <p className="mt-1 truncate text-body font-semibold text-ink">{run.title}</p>
      {/* The figure and time drop under the bar when the card's widest pair leaves it under 6rem. */}
      <div ref={lineRef} className="mt-2 flex flex-wrap items-baseline gap-x-3 gap-y-2">
        <div
          role="progressbar"
          aria-label={barWords(run.ticket, minutes, limit)}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={percent}
          className="h-1.5 min-w-0 shrink grow-10000 basis-24 overflow-hidden rounded-tag bg-track"
        >
          <div
            className={`h-full rounded-tag${percent > 0 ? " min-w-0.5" : ""} ${percent >= DEFER_FROM_PERCENT ? "bg-defer" : "bg-accent"}`}
            style={{ width: `${percent}%` }}
          />
        </div>
        <div className="flex min-w-0 grow flex-wrap justify-between gap-x-3 gap-y-1">
          <span
            className={`max-w-full font-mono text-label font-semibold ${wrapped ? "text-left" : "text-right"} ${over > 0 ? "text-defer" : "text-ink"}`}
            style={{ width: `${chars.figure}ch` }}
          >
            {figureWords(minutes, limit)}
          </span>
          <span
            className="ml-auto max-w-full text-right font-mono text-cap text-muted"
            style={{ width: `${chars.time}ch` }}
          >
            {timeWords(minutes, limit)}
          </span>
        </div>
      </div>
    </li>
  );
}

type ListProps = {
  // Every run in scope, so the columns are sized alike however many the list shows.
  runs: readonly RunningRun[];
  // How many of them the list draws.
  count: number;
  now: number;
  named: boolean;
  projects: readonly Project[];
};

// RunningList is the rows of the runs the card draws, their figure and time columns sized from
// every run in scope.
export function RunningList({ runs, count, now, named, projects }: ListProps) {
  const chars = columnChars(runs, now);
  return (
    <ul>
      {runs.slice(0, count).map((run) => (
        <RunningRow
          key={`${run.repo} ${run.ticket} ${run.stage}`}
          run={run}
          now={now}
          named={named}
          projects={projects}
          chars={chars}
        />
      ))}
    </ul>
  );
}

// LoadingRows is the three skeleton rows the first read draws, shaped like the rows they stand in
// for: a stage chip, a ticket and a project over a title and a bar.
function LoadingRows() {
  return (
    <div aria-hidden="true" className="card-frame mt-4 pb-1.25">
      {[0, 1, 2].map((row) => (
        <div key={row} className={`py-3 ${row === 0 ? "" : "border-t border-hair"}`}>
          <div className="flex items-center gap-2.5">
            <span className="h-5.5 w-17 shrink-0 rounded-full bg-track" />
            <span className="h-3 w-14 shrink-0 rounded-tag bg-track" />
            <span className="h-3 w-16 shrink-0 rounded-tag bg-track" />
          </div>
          <span className="mt-2 block h-3.5 w-3/4 rounded-tag bg-track" />
          <div className="mt-2.5 flex items-center gap-3">
            <span className="h-1.5 min-w-0 flex-1 rounded-tag bg-track" />
            {/* The drawn skeleton's figure and time columns, 31.2px and 72px. */}
            <span className="flex w-[31.2px] shrink-0 justify-end">
              <span className="h-3.5 w-7 rounded-tag bg-track" />
            </span>
            <span className="flex w-18 shrink-0 justify-end">
              <span className="h-3 w-14 rounded-tag bg-track" />
            </span>
          </div>
        </div>
      ))}
    </div>
  );
}

type SectionProps = {
  read: RunningRead;
  // The Overview's project list: the rows' project names, and the queued line's counts.
  projects: ProjectsState;
  // The page's read control, drawn at the end of the heading row.
  refresh: PageRefresh;
  className: string;
};

/**
 * RunningSection is the Overview's RUNNING NOW section: the runs the dispatcher has claimed and not
 * settled, the longest running first, each with a bar of the time its stage has used against the run's
 * own limit. A project chosen in the filter narrows it at once, and its rows drop the project name.
 */
export function RunningSection({ read, projects, refresh, className }: SectionProps) {
  const { state, retry } = read;
  const { project } = useProjectFilter();
  const headingId = useId();
  const [open, setOpen] = useState(false);

  const runs =
    state.status === "ready"
      ? state.runs.filter((run) => project === null || sameRepository(run.repo, project))
      : [];
  // The longest running first, whatever order the answer holds; a tie keeps the server's order.
  runs.sort((a, b) => Date.parse(a.claimed_at) - Date.parse(b.claimed_at));
  const folds = runs.length > SHOWN_RUNS;
  if (!folds && open) {
    setOpen(false);
  }

  const chosen = project === null ? null : displayName(project, projects.projects);
  const heading = ["running now", ...(chosen === null ? [] : [chosen])].join(", ");

  return (
    <section
      aria-labelledby={headingId}
      aria-busy={state.status === "loading" ? true : undefined}
      className={`min-w-0 ${className}`}
    >
      <div>
        <div className="flex min-h-6.5 items-start gap-x-4">
          <h2
            id={headingId}
            className="min-w-0 flex-1 py-[3.25px] font-mono text-label font-semibold text-ink2 uppercase wrap-break-word"
          >
            {heading}
          </h2>
          {refresh.readAt > 0 && (
            <span
              aria-busy={refresh.fetching ? true : undefined}
              className="flex shrink-0 items-center gap-1"
            >
              <RefreshClock refresh={refresh} />
              <RefreshButton refresh={refresh} />
            </span>
          )}
        </div>
        <RefreshNotice refresh={refresh} />
      </div>
      {/* Everything under the heading and its notice, as one child. */}
      <div>
        {state.status === "loading" && <LoadingRows />}
        {state.status === "error" && (
          <div className="mt-4">
            <p role="alert" className="text-body">
              The runs in flight could not be read. Runs already started keep going.
            </p>
            <TryAgainPill onClick={retry} />
          </div>
        )}
        {state.status === "ready" && (
          <div className="card-frame mt-4 pb-1.25">
            {runs.length === 0 ? (
              <p className="flex min-h-13 items-center text-body">
                <span className="min-w-0 wrap-break-word">
                  {chosen === null ? "Nothing running" : `Nothing running for ${chosen}`}
                </span>
              </p>
            ) : (
              <RunningList
                runs={runs}
                count={folds && !open ? SHOWN_RUNS : runs.length}
                now={state.readAt}
                named={project === null}
                projects={projects.projects}
              />
            )}
            {folds && (
              <DisclosureRow
                open={open}
                label={`Show all ${runs.length} running`}
                onClick={() => setOpen((was) => !was)}
              />
            )}
          </div>
        )}
        {state.status === "loading" && (
          <p className="mt-2 hidden text-cap leading-4 text-muted @[51.25rem]:block">
            Reading the runs in flight…
          </p>
        )}
        {state.status === "ready" && projects.status === "ready" && (
          <p className="mt-2 hidden text-cap leading-4 text-muted @[51.25rem]:block">
            {queueWords(countsOf(projects, project))}
          </p>
        )}
      </div>
    </section>
  );
}
