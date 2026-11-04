import { isNumber, isString, nullable, shape } from "../autonomy/guard";

/**
 * Recommendation is the N the concurrency rule recommends beside the owner's, with the evidence
 * its reason is worded from (internal/dispatcher/concurrency.go): the basis, the stable runs in a
 * row at N, and the rule's numbers.
 */
export type Recommendation = {
  n: number;
  basis: string;
  stable: number;
  stable_runs: number;
  rise_within: number;
  halve_beyond: number;
};

/**
 * RunsAtOnceView is the runs-at-once endpoint's answer: the owner's N, its upper bound, and the
 * recommendation beside it, null when that could not be read.
 */
export type RunsAtOnceView = {
  runs_at_once: number;
  max: number;
  recommendation: Recommendation | null;
};

const isRecommendation = shape<Recommendation>({
  n: isNumber,
  basis: isString,
  stable: isNumber,
  stable_runs: isNumber,
  rise_within: isNumber,
  halve_beyond: isNumber,
});

/** isRunsAtOnceView narrows the runs-at-once endpoint's answer. */
export const isRunsAtOnceView = shape<RunsAtOnceView>({
  runs_at_once: isNumber,
  max: isNumber,
  recommendation: nullable(isRecommendation),
});

/** N's lower bound; its upper bound is the view's max. */
export const MIN_RUNS_AT_ONCE = 1;

// reason is why the rule recommends r.n beside the owner's saved n, from the evidence at n.
function reason(r: Recommendation, n: number, max: number): string {
  if (r.basis === "slow") {
    return `a run at ${n} took more than ${r.halve_beyond}x the solo median, so it halves to ${r.n}.`;
  }
  if (r.basis === "rate-limited") {
    return `a run at ${n} was stopped by the provider's rate limit, so it halves to ${r.n}.`;
  }
  if (r.basis === "rise" && r.n > n) {
    return `${r.stable_runs} runs in a row at ${n} finished within ${r.rise_within}x the solo median, so one more can run at once.`;
  }
  if (n >= max) {
    return `runs at once cannot go above ${max}.`;
  }
  if (r.basis === "none" || r.stable === 0) {
    return `no run has finished at ${n} yet; after ${r.stable_runs} in a row it recommends ${n + 1}.`;
  }
  const runs = r.stable === 1 ? "run" : "runs";
  return `${r.stable} ${runs} in a row at ${n} finished within ${r.rise_within}x the solo median; after ${r.stable_runs} it recommends ${n + 1}.`;
}

/** noteWords is the recommendation note under the stepper, worded from the saved n. */
export function noteWords(r: Recommendation, n: number, max: number): string {
  return `Recommended: ${r.n}, because ${reason(r, n, max)}`;
}
