import { isNumber, isString, listOf, nullable, shape } from "../autonomy/guard";
import { sydneyDateTime } from "../sydney-date-time";

// One run's cache figures as the comparison endpoint serves them: the whole
// run's own share, and each phase's share of its first request and of its whole
// input, null wherever the run recorded none of it.
export type RunCache = {
  run_id: string;
  provider: string;
  cached_share: number;
  build_cache: string;
  plan_cache: string;
  build_first_share: number | null;
  build_phase_share: number | null;
  plan_first_share: number | null;
  plan_phase_share: number | null;
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
      build_phase_share: nullable(isNumber),
      plan_first_share: nullable(isNumber),
      plan_phase_share: nullable(isNumber),
      settled_at: isString,
    }),
  ),
});

export type RunsState = {
  status: "loading" | "error" | "ready";
  runs: RunCache[];
};

// The two phases the cache table prints a first-request and a whole-phase share
// for, in column order.
export type CachedPhase = "build" | "plan";

type PhaseFigures = { first: number | null; whole: number | null; word: string };

// The word a phase that made no request in a run reads as. The endpoint words
// that "none" for either phase, so each phase names itself instead.
const NO_STAGE_WORD: Record<CachedPhase, string> = {
  build: "no build stage",
  plan: "no plan stage",
};

// figuresOf is one phase's two shares from the endpoint's numbers, or the word
// the run recorded instead of them. The endpoint measures a phase as a pair,
// both shares or neither, so a lone share is not a measurement.
export function figuresOf(run: RunCache, phase: CachedPhase): PhaseFigures {
  const first = phase === "build" ? run.build_first_share : run.plan_first_share;
  const whole = phase === "build" ? run.build_phase_share : run.plan_phase_share;
  if (first !== null && whole !== null) {
    return { first, whole, word: "" };
  }
  const recorded = phase === "build" ? run.build_cache : run.plan_cache;
  return {
    first: null,
    whole: null,
    word: recorded === "none" ? NO_STAGE_WORD[phase] : recorded,
  };
}

// finishedOf is when a run settled on Sydney's clock ("30 Oct, 11:42"), or null where the endpoint
// recorded no instant or served one that is not a date: the zero time is how a run with none reads.
export function finishedOf(run: RunCache): string | null {
  const at = Date.parse(run.settled_at);
  if (Number.isNaN(at) || new Date(at).getUTCFullYear() <= 1) {
    return null;
  }
  return sydneyDateTime(run.settled_at);
}

// partitionRuns splits the runs into those that carry a per-phase split and the older ones that
// collapse to one counted line.
export function partitionRuns(runs: RunCache[]): { split: RunCache[]; older: RunCache[] } {
  return { split: runs.filter(hasSplit), older: runs.filter((run) => !hasSplit(run)) };
}

// hasSplit is whether at least one phase carries its pair of shares, which is
// how the endpoint measures a phase. A lone share is not a measurement, so a run
// holding no pair predates the split and collapses to one line.
export function hasSplit(run: RunCache): boolean {
  return (
    (run.build_first_share !== null && run.build_phase_share !== null) ||
    (run.plan_first_share !== null && run.plan_phase_share !== null)
  );
}
