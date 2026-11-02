import { describe, expect, it } from "bun:test";
import {
  barWords,
  columnChars,
  figureWords,
  isRunning,
  overdueBy,
  percentOf,
  timeWords,
  type RunningRun,
} from "./run";

const NOW = Date.UTC(2026, 2, 1, 12, 0, 0);

function run(minutes: number, limit: number): RunningRun {
  return {
    ticket: "DEMO-1",
    repo: "acme/widgets",
    stage: "building",
    claimed_at: new Date(NOW - minutes * 60_000).toISOString(),
    title: "",
    limit_minutes: limit,
  };
}

describe("isRunning", () => {
  it("accepts a run that carries a whole limit", () => {
    expect(isRunning({ runs: [run(5, 60)] })).toBe(true);
  });

  it("rejects the whole read for one run without a limit, with zero, a fraction or a string", () => {
    const { limit_minutes: _limit, ...bare } = run(5, 60);
    expect(isRunning({ runs: [run(5, 60), bare] })).toBe(false);
    expect(isRunning({ runs: [run(5, 0)] })).toBe(false);
    expect(isRunning({ runs: [run(5, 7.5)] })).toBe(false);
    expect(isRunning({ runs: [{ ...run(5, 60), limit_minutes: "90" }] })).toBe(false);
  });
});

describe("percentOf", () => {
  it("rounds the share of the limit down", () => {
    expect(percentOf(0, 60)).toBe(0);
    expect(percentOf(47, 60)).toBe(78);
    expect(percentOf(71, 90)).toBe(78);
  });

  it("reads 80 from the minute that reaches it and 98 just below the limit", () => {
    expect(percentOf(48, 60)).toBe(80);
    expect(percentOf(72, 90)).toBe(80);
    expect(percentOf(59, 60)).toBe(98);
    expect(percentOf(89, 90)).toBe(98);
  });

  it("holds at 100 from the limit on", () => {
    expect(percentOf(60, 60)).toBe(100);
    expect(percentOf(61, 60)).toBe(100);
    expect(percentOf(113, 90)).toBe(100);
  });

  it("divides whole numbers, where a float quotient reads a percent short", () => {
    expect(percentOf(23, 5)).toBe(100);
    expect(percentOf(29, 100)).toBe(29);
    expect(percentOf(57, 100)).toBe(57);
  });
});

describe("overdueBy", () => {
  it("is zero at the limit and one a minute past it", () => {
    expect(overdueBy(89, 90)).toBe(0);
    expect(overdueBy(90, 90)).toBe(0);
    expect(overdueBy(91, 90)).toBe(1);
    expect(overdueBy(113, 90)).toBe(23);
  });
});

describe("figureWords", () => {
  it("reads the percent up to the limit and the minutes past it", () => {
    expect(figureWords(90, 90)).toBe("100%");
    expect(figureWords(91, 90)).toBe("overdue +1m");
    expect(figureWords(113, 90)).toBe("overdue +23m");
    expect(figureWords(66, 60)).toBe("overdue +6m");
  });
});

describe("timeWords", () => {
  it("reads minutes only, at any length, against the run's own limit", () => {
    expect(timeWords(41, 60)).toBe("41m of 60m");
    expect(timeWords(41, 90)).toBe("41m of 90m");
    expect(timeWords(113, 90)).toBe("113m of 90m");
    expect(timeWords(0, 60)).toBe("0m of 60m");
  });
});

describe("barWords", () => {
  it("names the minutes used and the share of the limit", () => {
    expect(barWords("DEMO-74", 76, 90)).toBe(
      "DEMO-74: 76 minutes used, 84 percent of the 90 minute limit",
    );
  });

  it("names the minutes past the limit with no percent", () => {
    expect(barWords("DEMO-71", 113, 90)).toBe(
      "DEMO-71: 113 minutes used, 23 minutes over the 90 minute limit",
    );
  });

  it("uses the singular for one minute", () => {
    expect(barWords("DEMO-1", 1, 60)).toBe(
      "DEMO-1: 1 minute used, 1 percent of the 60 minute limit",
    );
    expect(barWords("DEMO-1", 91, 90)).toBe(
      "DEMO-1: 91 minutes used, 1 minute over the 90 minute limit",
    );
  });
});

describe("columnChars", () => {
  it("takes the longest figure and time across every run", () => {
    const runs = [run(12, 60), run(113, 90), run(5, 60)];
    expect(columnChars(runs, NOW)).toEqual({
      figure: "overdue +23m".length,
      time: "113m of 90m".length,
    });
  });

  it("measures runs inside their limit by their percent and time", () => {
    expect(columnChars([run(5, 60), run(41, 90)], NOW)).toEqual({
      figure: 3,
      time: "41m of 90m".length,
    });
  });
});
