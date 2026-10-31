/** One run in flight, as GET /api/running serves it. */
export type RunningRun = {
  ticket: string;
  repo: string;
  stage: string;
  // When the dispatcher claimed the run, RFC 3339 in UTC.
  claimed_at: string;
  // The ticket's title, empty when the run's row records none.
  title: string;
};

// The run limit each bar is measured against, in minutes: the runner's own budget for a stage.
export const RUN_LIMIT_MINUTES = 90;

// The percentage a bar turns defer at: the stage is close to the run limit.
export const DEFER_FROM_PERCENT = 80;

function isRunningRun(row: unknown): row is RunningRun {
  if (typeof row !== "object" || row === null) {
    return false;
  }
  const { ticket, repo, stage, claimed_at: claimedAt, title } = row as Record<string, unknown>;
  return (
    typeof ticket === "string" &&
    typeof repo === "string" &&
    typeof stage === "string" &&
    typeof claimedAt === "string" &&
    typeof title === "string"
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

/** percentOf is those minutes against the run limit, rounded down and held at 100. */
export function percentOf(minutes: number): number {
  return Math.min(100, Math.floor((minutes / RUN_LIMIT_MINUTES) * 100));
}

/** timeWords is a run's elapsed time: "28m" under an hour, "1h 16m" from one. */
export function timeWords(minutes: number): string {
  if (minutes < 60) {
    return `${minutes}m`;
  }
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

/** The projects' queued and held counts, narrowed to one project or summed over several. */
export type QueueCounts = { queued: number; held: number };

/** barWords is a bar's accessible name: the ticket, its minutes used and its percentage. */
export function barWords(ticket: string, minutes: number, percent: number): string {
  return `${ticket}: ${minutes} minutes used, ${percent} percent of the run limit`;
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
