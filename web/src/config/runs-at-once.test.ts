import { describe, expect, it } from "bun:test";
import {
  isRunsAtOnceView,
  parseRunsAtOnce,
  presetsUnder,
  rangeError,
  rangeSentence,
  reasonWords,
  type Recommendation,
} from "./runs-at-once";

const RULE = { stable_runs: 5, rise_within: 1.25, halve_beyond: 1.5 };

function rec(n: number, basis: string, stable = 0): Recommendation {
  return { n, basis, stable, ...RULE };
}

describe("reasonWords", () => {
  it("words each drawn reason from its evidence", () => {
    expect(reasonWords(rec(3, "rise", 5), 2, 20)).toBe(
      "5 runs in a row at 2 finished within 1.25x the solo median, so one more can run at once.",
    );
    expect(reasonWords(rec(1, "slow"), 2, 20)).toBe(
      "A run at 2 took more than 1.5x the solo median, so it halves to 1.",
    );
    expect(reasonWords(rec(2, "steady", 3), 2, 20)).toBe(
      "The same as yours. 3 runs in a row at 2 finished within 1.25x the solo median; after 5 it recommends 3.",
    );
    expect(reasonWords(rec(3, "none"), 3, 20)).toBe(
      "The same as yours. No run has finished at 3 yet; after 5 in a row it recommends 4.",
    );
  });

  it("says no run has finished while the count at N is 0", () => {
    expect(reasonWords(rec(2, "steady", 0), 2, 20)).toBe(
      "The same as yours. No run has finished at 2 yet; after 5 in a row it recommends 3.",
    );
  });

  it("says run for one run in a row", () => {
    expect(reasonWords(rec(2, "steady", 1), 2, 20)).toContain(" 1 run in a row at 2 ");
  });

  it("names a rate-limit stop rather than a slow run", () => {
    expect(reasonWords(rec(2, "rate-limited"), 4, 20)).toBe(
      "A run at 4 was stopped by the provider's rate limit, so it halves to 2.",
    );
  });

  it("recommends nothing past the platform cap", () => {
    expect(reasonWords(rec(20, "steady", 4), 20, 20)).toBe(
      "The same as yours: 20 is the platform cap.",
    );
  });
});

describe("parseRunsAtOnce", () => {
  it("reads a whole number from 1 to the cap", () => {
    expect(parseRunsAtOnce("1", 20)).toBe(1);
    expect(parseRunsAtOnce(" 20 ", 20)).toBe(20);
  });

  it("refuses anything else", () => {
    for (const text of ["", "0", "21", "2.5", "-1", "3a", "1e1"]) {
      expect(parseRunsAtOnce(text, 20)).toBeNull();
    }
    expect(parseRunsAtOnce("11", 10)).toBeNull();
  });
});

describe("the presets", () => {
  it("are 5, 10 and 15 under the default cap", () => {
    expect(presetsUnder(20)).toEqual([5, 10, 15]);
  });

  it("leave out a preset above the platform cap", () => {
    expect(presetsUnder(12)).toEqual([5, 10]);
    expect(presetsUnder(4)).toEqual([]);
  });
});

describe("the range words", () => {
  it("render the cap", () => {
    expect(rangeSentence(20)).toBe(
      "The most runs in flight at once, across every project: a whole number from 1 to 20.",
    );
    expect(rangeError(12)).toBe("Enter a whole number from 1 to 12.");
  });
});

describe("isRunsAtOnceView", () => {
  it("accepts a view with or without a recommendation", () => {
    expect(
      isRunsAtOnceView({ runs_at_once: 2, platform_cap: 20, recommendation: rec(3, "rise", 5) }),
    ).toBe(true);
    expect(isRunsAtOnceView({ runs_at_once: 2, platform_cap: 20, recommendation: null })).toBe(
      true,
    );
    expect(isRunsAtOnceView({ runs_at_once: 2, platform_cap: 20 })).toBe(false);
  });
});
