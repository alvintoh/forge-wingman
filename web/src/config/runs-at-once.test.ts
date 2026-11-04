import { describe, expect, it } from "bun:test";
import { isRunsAtOnceView, noteWords, type Recommendation } from "./runs-at-once";

const RULE = { stable_runs: 5, rise_within: 1.25, halve_beyond: 1.5 };

function rec(n: number, basis: string, stable = 0): Recommendation {
  return { n, basis, stable, ...RULE };
}

describe("noteWords", () => {
  it("words each drawn note from its evidence", () => {
    expect(noteWords(rec(2, "none"), 2, 15)).toBe(
      "Recommended: 2, because no run has finished at 2 yet; after 5 in a row it recommends 3.",
    );
    expect(noteWords(rec(3, "rise", 5), 2, 15)).toBe(
      "Recommended: 3, because 5 runs in a row at 2 finished within 1.25x the solo median, so one more can run at once.",
    );
    expect(noteWords(rec(1, "slow"), 2, 15)).toBe(
      "Recommended: 1, because a run at 2 took more than 1.5x the solo median, so it halves to 1.",
    );
  });

  it("says no run has finished while the count at N is 0", () => {
    expect(noteWords(rec(2, "steady", 0), 2, 15)).toBe(
      "Recommended: 2, because no run has finished at 2 yet; after 5 in a row it recommends 3.",
    );
  });

  it("counts the stable runs so far, singular for one", () => {
    expect(noteWords(rec(2, "steady", 3), 2, 15)).toBe(
      "Recommended: 2, because 3 runs in a row at 2 finished within 1.25x the solo median; after 5 it recommends 3.",
    );
    expect(noteWords(rec(2, "steady", 1), 2, 15)).toContain(" 1 run in a row at 2 ");
  });

  it("names a rate-limit stop rather than a slow run", () => {
    expect(noteWords(rec(2, "rate-limited"), 4, 15)).toBe(
      "Recommended: 2, because a run at 4 was stopped by the provider's rate limit, so it halves to 2.",
    );
  });

  it("recommends nothing past the max, even after a rise held to it", () => {
    expect(noteWords(rec(15, "steady", 4), 15, 15)).toBe(
      "Recommended: 15, because runs at once cannot go above 15.",
    );
    expect(noteWords(rec(15, "rise", 5), 15, 15)).toBe(
      "Recommended: 15, because runs at once cannot go above 15.",
    );
  });
});

describe("isRunsAtOnceView", () => {
  it("accepts a view with or without a recommendation", () => {
    expect(isRunsAtOnceView({ runs_at_once: 2, max: 15, recommendation: rec(3, "rise", 5) })).toBe(
      true,
    );
    expect(isRunsAtOnceView({ runs_at_once: 2, max: 15, recommendation: null })).toBe(true);
    expect(isRunsAtOnceView({ runs_at_once: 2, platform_cap: 20, recommendation: null })).toBe(
      false,
    );
  });
});
