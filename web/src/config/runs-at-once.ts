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
  platform_cap: number;
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
  platform_cap: isNumber,
  recommendation: nullable(isRecommendation),
});

/** The presets beside the field: one tap fills it with the number and saves nothing. */
export const RUNS_AT_ONCE_PRESETS = [5, 10, 15] as const;

/** presetsUnder is the presets the platform cap allows; one above it is never offered. */
export function presetsUnder(platformCap: number): number[] {
  return RUNS_AT_ONCE_PRESETS.filter((preset) => preset <= platformCap);
}

/** parseRunsAtOnce is the field's text as N, or null unless it is a whole number from 1 to the cap. */
export function parseRunsAtOnce(text: string, platformCap: number): number | null {
  if (!/^\d+$/.test(text.trim())) {
    return null;
  }
  const n = Number(text.trim());
  return n >= 1 && n <= platformCap ? n : null;
}

/** rangeSentence says what N is and its range, from the platform cap. */
export function rangeSentence(platformCap: number): string {
  return `The most runs in flight at once, across every project: a whole number from 1 to ${platformCap}.`;
}

/** rangeError is the words under a field holding no N the range allows. */
export function rangeError(platformCap: number): string {
  return `Enter a whole number from 1 to ${platformCap}.`;
}

/**
 * reasonWords is why the rule recommends r.n beside the owner's saved n, worded from the evidence
 * at n. A recommendation equal to n says how far the count has to go, unless n is the cap.
 */
export function reasonWords(r: Recommendation, n: number, platformCap: number): string {
  switch (r.basis) {
    case "rise":
      return `${r.stable_runs} runs in a row at ${n} finished within ${r.rise_within}x the solo median, so one more can run at once.`;
    case "slow":
      return `A run at ${n} took more than ${r.halve_beyond}x the solo median, so it halves to ${r.n}.`;
    case "rate-limited":
      return `A run at ${n} was stopped by the provider's rate limit, so it halves to ${r.n}.`;
  }
  if (n >= platformCap) {
    return `The same as yours: ${platformCap} is the platform cap.`;
  }
  if (r.basis === "none" || r.stable === 0) {
    return `The same as yours. No run has finished at ${n} yet; after ${r.stable_runs} in a row it recommends ${n + 1}.`;
  }
  const runs = r.stable === 1 ? "run" : "runs";
  return `The same as yours. ${r.stable} ${runs} in a row at ${n} finished within ${r.rise_within}x the solo median; after ${r.stable_runs} it recommends ${n + 1}.`;
}
