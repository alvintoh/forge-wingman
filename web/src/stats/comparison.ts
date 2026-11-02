import { isNumber, isString, listOf, nullable, shape } from "../autonomy/guard";
import { sydneyDateTime } from "../sydney-date-time";

// One run's cache figures as the comparison endpoint serves them: the whole run's own share, and
// each phase's share of its first request, null wherever the run recorded none of it.
export type RunCache = {
  run_id: string;
  provider: string;
  cached_share: number;
  build_cache: string;
  plan_cache: string;
  build_first_share: number | null;
  plan_first_share: number | null;
  // When the run settled, an RFC 3339 instant; the zero instant where none was recorded.
  settled_at: string;
};

export type ComparisonResponse = { runs: RunCache[] };

// isComparison narrows the endpoint's answer to the runs the page draws.
export const isComparison = shape<ComparisonResponse>({
  runs: listOf(
    shape<RunCache>({
      run_id: isString,
      provider: isString,
      cached_share: isNumber,
      build_cache: isString,
      plan_cache: isString,
      build_first_share: nullable(isNumber),
      plan_first_share: nullable(isNumber),
      settled_at: isString,
    }),
  ),
});

export type RunsState = {
  status: "loading" | "error" | "ready";
  runs: RunCache[];
};

// The two phases the cache table prints a first-request share for, in column order.
export type CachedPhase = "build" | "plan";

export type PhaseCell =
  | { kind: "share"; share: number }
  | { kind: "none" }
  | { kind: "unrecorded" };

// phaseCell is one phase's cell: its first-request share, else "none" where the endpoint's word is
// "none", else unrecorded. Only the word's equality with "none" is read.
export function phaseCell(run: RunCache, phase: CachedPhase): PhaseCell {
  const share = phase === "build" ? run.build_first_share : run.plan_first_share;
  if (share !== null) {
    return { kind: "share", share };
  }
  const word = phase === "build" ? run.build_cache : run.plan_cache;
  return { kind: word === "none" ? "none" : "unrecorded" };
}

// finishedOf is when a run settled on Sydney's clock ("30 Oct 2026, 11:42"), or null where the
// endpoint recorded no instant or served one that is not a date; the zero time reads as none.
export function finishedOf(run: RunCache): string | null {
  const at = Date.parse(run.settled_at);
  if (Number.isNaN(at) || new Date(at).getUTCFullYear() <= 1) {
    return null;
  }
  return sydneyDateTime(run.settled_at);
}

export type ColumnChars = { runId: number; provider: number; finished: number };

// columnChars is the most characters each text column holds across every run, so the columns keep
// one width on every page.
export function columnChars(runs: RunCache[]): ColumnChars {
  const chars: ColumnChars = { runId: 0, provider: 0, finished: 0 };
  for (const run of runs) {
    chars.runId = Math.max(chars.runId, run.run_id.length);
    chars.provider = Math.max(chars.provider, run.provider.length);
    chars.finished = Math.max(chars.finished, finishedOf(run)?.length ?? 0);
  }
  return chars;
}
