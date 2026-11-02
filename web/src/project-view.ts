import { ago } from "./ago";
import type { PhaseProgress, PhaseSnapshot, Project } from "./project-list";

export const NOTHING_TO_START =
  "Cannot start as no ticket in this phase is ready to delegate. Please resolve them to start.";

const REACH_REACHABLE = "reachable";
const REACH_CHECKING = "checking";

// The refusals the dispatcher records for a start it did not carry out.
const refusalWords: Record<string, string> = {
  "phase-moved": "The last start was not carried out: the phase moved on.",
  "linear-unreadable": "The last start was not carried out: Linear could not be read.",
  "no-eligible-ticket": "The last start was not carried out: no ticket was ready to start.",
  "repository-unreachable":
    "The last start was not carried out: the repository could not be reached.",
  "no-current-phase": "The last start was not carried out: the project has no current phase.",
  "repository-unchecked":
    "The last start was not carried out: the repository had not been checked yet.",
};

export type StatePill = "CHECKING" | "STOPPING" | "STOPPED" | "RUNNING" | null;

// The project's state word, or null while it is idle.
export function projectState({ paused, queue, reach, snapshot }: Project): StatePill {
  if (reach.status === REACH_CHECKING) {
    return "CHECKING";
  }
  if (paused) {
    return queue.in_flight > 0 ? "STOPPING" : "STOPPED";
  }
  const queued = snapshot !== null && !snapshot.unreadable ? snapshot.queued : 0;
  return queue.in_flight > 0 || queued > 0 ? "RUNNING" : null;
}

// One status line of a card. The phone card keeps only the line marked `keep` (what is moving);
// the others show from md up and sit behind Details on a phone.
export type StatusLine = { text: string; keep: boolean };

// The view of one project: derived here from the list's fields alone.
export type ProjectView = {
  pill: StatePill;
  readable: PhaseSnapshot | null;
  phase: PhaseProgress | null;
  lines: StatusLine[];
  startable: boolean;
  notice: string | null;
  startDisabled: boolean;
};

export function describe(project: Project, now: number): ProjectView {
  const { paused, queue, reach } = project;
  const snapshot = project.snapshot;
  const readable = snapshot !== null && !snapshot.unreadable ? snapshot : null;
  const queued = readable?.queued ?? 0;
  const working = queue.in_flight > 0 || queued > 0;

  const pill = projectState(project);

  const phases = readable?.phases ?? [];
  const index = phases.findIndex((p) => p.name === readable?.current);
  const phase = index >= 0 ? (phases[index] ?? null) : null;
  const activity = project.last_activity
    ? `Active ${ago(project.last_activity, now)}`
    : `Added ${ago(project.added_at, now)}`;

  const lines: StatusLine[] = [];
  const line = (text: string, keep = false) => lines.push({ text, keep });
  if (readable !== null && phases.length === 0) {
    line(`No phases yet; ${activity.charAt(0).toLowerCase()}${activity.slice(1)}`, true);
  } else if (readable !== null) {
    const later = index >= 0 ? phases.length - 1 - index : 0;
    const locked =
      later === 0
        ? null
        : later === 1
          ? `Phase ${phases.length} locked`
          : `Phases ${index + 2}-${phases.length} locked`;
    if (pill === "STOPPING") {
      line("No new tickets start");
      line(`${queue.in_flight} finishing, ${queue.held} queued held`, true);
    } else {
      if (phase !== null) {
        line(`${phase.done} of ${phase.total} tickets done`);
      }
      if (pill === "STOPPED" && queue.held > 0) {
        line(`${queue.held} queued, held until resumed`, true);
      } else if (working) {
        line(`${queue.in_flight} in flight, ${queued} queued`, true);
      } else if (locked !== null) {
        line(locked, true);
      }
    }
    line(activity);
  } else {
    line(activity, true);
  }
  // A phone card always shows one status line: the last line when none is marked.
  if (!lines.some((l) => l.keep)) {
    lines.splice(-1, 1, { text: lines[lines.length - 1]?.text ?? activity, keep: true });
  }

  const quiet =
    readable !== null &&
    !paused &&
    queue.in_flight === 0 &&
    queue.held === 0 &&
    queued === 0 &&
    readable.in_flight.length === 0 &&
    !project.start_requested_at;
  const idle = quiet && (readable?.eligible.length ?? 0) > 0;
  const startable = idle && reach.status === REACH_REACHABLE;
  const nothingToStart =
    quiet && readable?.eligible.length === 0 && phase !== null && reach.status === REACH_REACHABLE;

  let notice: string | null = null;
  if (project.start_requested_at) {
    notice = "Start requested, delegates on the next poll.";
  } else if (reach.status === REACH_CHECKING) {
    notice = "Checking on the next poll.";
  } else if (reach.status !== REACH_REACHABLE) {
    notice = unreachableWords(project);
  } else if (snapshot === null) {
    notice = "The phases have not been read yet.";
  } else if (project.start_refusal) {
    notice = refusalWords[project.start_refusal] ?? "The last start was not carried out.";
  } else if (nothingToStart) {
    notice = NOTHING_TO_START;
  }

  return {
    pill,
    readable,
    phase,
    lines,
    startable,
    notice,
    startDisabled: (idle && !startable) || nothingToStart,
  };
}

// unreachableWords says why the dispatcher could not read the repository, from
// the closed set of reasons it records.
function unreachableWords(project: Project): string {
  const { repository, reach } = project;
  if (reach.reason === "not-found") {
    return `${repository} was not found, or the dispatcher's token cannot see it. Check its name and access on GitHub to start a phase.`;
  }
  if (reach.reason === "forbidden" || reach.reason === "unauthorized") {
    return `The dispatcher's token cannot read ${repository}. Check its access on GitHub to start a phase.`;
  }
  return `The dispatcher could not reach ${repository}. It is checked again on the next poll.`;
}
