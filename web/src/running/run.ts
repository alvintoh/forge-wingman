import { ago } from "../ago";

/** One run in flight, as GET /api/running serves it. */
export type RunningRun = {
  ticket: string;
  repo: string;
  stage: string;
  // When the dispatcher claimed the run, RFC 3339 in UTC.
  claimed_at: string;
  // The ticket's title, empty when the run's row records none.
  title: string;
  // The minutes the run's stage may take.
  limit_minutes: number;
  // The run's newest progress report, read through progressWords: absent or null where it wrote
  // none, and whatever the endpoint sent where the report was malformed.
  progress?: unknown;
};

// The percentage a bar turns defer at: the stage is close to the run limit.
export const DEFER_FROM_PERCENT = 80;

// The age past which a run's newest progress reads as stalled rather than recent, in seconds: two
// missed writes of the runner's report (progressInterval, internal/runner/progress_report.go), so
// the two change together.
const STALE_AFTER_SECONDS = 6 * 60;

// The part of a run's newest progress report the row draws: its step count, the diff's added and
// removed lines, and how long ago the report was written. The report the endpoint serves also
// carries its phase, which the caption does not show.
type RunningProgress = {
  steps: number;
  lines: number;
  age_seconds: number;
};

function isRunningRun(row: unknown): row is RunningRun {
  if (typeof row !== "object" || row === null) {
    return false;
  }
  const {
    ticket,
    repo,
    stage,
    claimed_at: claimedAt,
    title,
    limit_minutes: limit,
  } = row as Record<string, unknown>;
  return (
    typeof ticket === "string" &&
    typeof repo === "string" &&
    typeof stage === "string" &&
    typeof claimedAt === "string" &&
    Number.isFinite(Date.parse(claimedAt)) &&
    typeof title === "string" &&
    typeof limit === "number" &&
    Number.isInteger(limit) &&
    limit > 0
  );
}

/**
 * isRunning is the guard for the running endpoint's answer: every entry carries every field, so a
 * row the page cannot draw never reaches it.
 */
export function isRunning(body: unknown): body is { runs: RunningRun[] } {
  if (typeof body !== "object" || body === null || !("runs" in body)) {
    return false;
  }
  return Array.isArray(body.runs) && body.runs.every(isRunningRun);
}

/** minutesOf is the whole minutes from the entry's claim to now, never negative. */
export function minutesOf(claimedAt: string, now: number): number {
  return Math.max(0, Math.floor((now - Date.parse(claimedAt)) / 60_000));
}

/** percentOf is those minutes against the run's limit, rounded down and held at 100. */
export function percentOf(minutes: number, limit: number): number {
  return Math.min(100, Math.floor((minutes * 100) / limit));
}

/** overdueBy is the whole minutes past the limit, zero up to and at it. */
export function overdueBy(minutes: number, limit: number): number {
  return Math.max(0, minutes - limit);
}

/** figureWords is the bar's figure: the percent of the limit, or the minutes past it. */
export function figureWords(minutes: number, limit: number): string {
  const over = overdueBy(minutes, limit);
  return over > 0 ? `overdue +${over}m` : `${percentOf(minutes, limit)}%`;
}

/** timeWords is a run's elapsed minutes against its limit: "41m of 60m". */
export function timeWords(minutes: number, limit: number): string {
  return `${minutes}m of ${limit}m`;
}

// isProgress is the guard for one report: every count a whole number from 0, as the endpoint
// bounds them. A report that fails it is one the row draws as absent.
function isProgress(value: unknown): value is RunningProgress {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const report = value as Record<string, unknown>;
  return [report.steps, report.lines, report.age_seconds].every(
    (count) => typeof count === "number" && Number.isInteger(count) && count >= 0,
  );
}

/** A run's caption: the counts its newest report carries, then how long ago it was written. */
export type ProgressWords = {
  // "Step 14 · 128 lines", or "No progress yet".
  lead: string;
  // "2m ago", or "no update for 21m" once the report is stale; empty with no progress.
  age: string;
  // Whether the age is stale, and so reads in the defer colour.
  stale: boolean;
  // The whole caption, lead and age, for the tooltip a cut caption carries.
  full: string;
};

/**
 * progressWords is the caption under a run's bar: its step count and diff lines, then the age of
 * its newest report in the app's ago form, or "No progress yet" for a run that has written none.
 * A report the endpoint sent malformed draws as none rather than failing the read.
 */
export function progressWords(progress: unknown, now: number): ProgressWords {
  if (!isProgress(progress)) {
    return { lead: "No progress yet", age: "", stale: false, full: "No progress yet" };
  }
  const stale = progress.age_seconds > STALE_AFTER_SECONDS;
  const at = now - progress.age_seconds * 1000;
  const lead = `Step ${progress.steps} · ${plural(progress.lines, "line")}`;
  const age = stale ? `no update for ${ago(at, now, "")}` : ago(at, now);
  return { lead, age, stale, full: `${lead} · ${age}` };
}

/** The longest figure and time, in characters, across the runs of one card. */
export type ColumnChars = { figure: number; time: number };

/** columnChars sizes the card's figure and time columns from every run in scope. */
export function columnChars(runs: readonly RunningRun[], now: number): ColumnChars {
  let figure = 0;
  let time = 0;
  for (const { claimed_at: claimedAt, limit_minutes: limit } of runs) {
    const minutes = minutesOf(claimedAt, now);
    figure = Math.max(figure, figureWords(minutes, limit).length);
    time = Math.max(time, timeWords(minutes, limit).length);
  }
  return { figure, time };
}

/** The projects' queued and held counts, narrowed to one project or summed over several. */
export type QueueCounts = { queued: number; held: number };

/** barWords is a bar's accessible name: the minutes used and the share of the limit or the minutes past it. */
export function barWords(ticket: string, minutes: number, limit: number): string {
  const over = overdueBy(minutes, limit);
  const standing =
    over > 0
      ? `${plural(over, "minute")} over the ${limit} minute limit`
      : `${percentOf(minutes, limit)} percent of the ${limit} minute limit`;
  return `${ticket}: ${plural(minutes, "minute")} used, ${standing}`;
}

function plural(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? "" : "s"}`;
}

/**
 * queueWords is the muted line under the runs: what the projects have queued, then what a stopped
 * or stopping one holds back until it is resumed. A project holding its queue back says nothing
 * about a queue it does not have.
 */
export function queueWords({ queued, held }: QueueCounts): string {
  if (held === 0) {
    return `${queued} queued`;
  }
  return queued === 0
    ? `${held} held until resumed`
    : `${queued} queued, ${held} held until resumed`;
}
