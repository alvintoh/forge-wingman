import { useId, useState } from "react";
import { DisclosureRow } from "../disclosure-row";
import { TryAgainPill } from "../pill-button";
import { useProjectFilter } from "../project-filter";
import { displayName, type Project, type ProjectsState } from "../project-list";
import { sameRepository } from "../project-search";
import type { RunningRead } from "./read";
import {
  DEFER_FROM_PERCENT,
  type QueueCounts,
  RUN_LIMIT_MINUTES,
  type RunningRun,
  barWords,
  minutesOf,
  percentOf,
  queueWords,
  timeWords,
} from "./run";

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
    if (project.snapshot !== null && !project.snapshot.unreadable) {
      queued += project.snapshot.queued;
    }
    if (project.paused) {
      held += project.queue.held;
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
};

// RunningRow is one run in flight: its stage, ticket and project over its title, then the bar of the
// time its stage has used. The row goes nowhere: a run in flight has no PR, and the bar is time,
// never work done.
function RunningRow({ run, now, named, projects }: RowProps) {
  const minutes = minutesOf(run.claimed_at, now);
  const percent = percentOf(minutes);
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
      <div className="mt-2 flex items-baseline gap-3">
        <div
          role="progressbar"
          aria-label={barWords(run.ticket, minutes, percent)}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={percent}
          className="h-1.5 min-w-0 flex-1 overflow-hidden rounded-tag bg-track"
        >
          <div
            className={`h-full rounded-tag ${percent >= DEFER_FROM_PERCENT ? "bg-defer" : "bg-accent"}`}
            style={{ width: `${percent}%` }}
          />
        </div>
        <span className="shrink-0 font-mono text-label font-semibold text-ink">{percent}%</span>
        <span className="shrink-0 font-mono text-label text-muted">{timeWords(minutes)}</span>
      </div>
    </li>
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
            <span className="h-3 w-8 shrink-0 rounded-tag bg-track" />
            <span className="h-3.5 w-12 shrink-0 rounded-tag bg-track" />
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
};

/**
 * RunningSection is the Overview's RUNNING NOW section: the runs the dispatcher has claimed and not
 * settled, the longest running first, each with a bar of the time its stage has used against the run
 * limit. A project chosen in the filter narrows it at once, and its rows drop the project name.
 */
export function RunningSection({ read, projects }: SectionProps) {
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
  const listed = folds && !open ? runs.slice(0, SHOWN_RUNS) : runs;

  const chosen = project === null ? null : displayName(project, projects.projects);
  const heading = ["running now", ...(chosen === null ? [] : [chosen])].join(", ");

  return (
    <section
      aria-labelledby={headingId}
      aria-busy={state.status === "loading" ? true : undefined}
      className="min-w-0"
    >
      <div className="flex min-h-6.5 flex-wrap items-start justify-between gap-x-4">
        <h2
          id={headingId}
          className="min-w-0 py-[3.25px] font-mono text-label font-semibold text-ink2 uppercase wrap-break-word"
        >
          {heading}
        </h2>
        <p className="ml-auto py-[3.25px] text-cap whitespace-nowrap text-muted">
          {`bar = % of ${RUN_LIMIT_MINUTES}m run limit`}
        </p>
      </div>
      {/* The section's second row: everything under the heading, as one child. */}
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
                {chosen === null ? "Nothing running" : `Nothing running for ${chosen}`}
              </p>
            ) : (
              <ul>
                {listed.map((run) => (
                  <RunningRow
                    key={`${run.repo} ${run.ticket} ${run.stage}`}
                    run={run}
                    now={state.readAt}
                    named={project === null}
                    projects={projects.projects}
                  />
                ))}
              </ul>
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
