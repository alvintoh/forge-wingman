import { describe, expect, it } from "bun:test";
import {
  barWords,
  columnChars,
  figureWords,
  isRunning,
  overdueBy,
  percentOf,
  progressWords,
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

  it("accepts a run whose progress is malformed, so one report cannot reject the read", () => {
    expect(isRunning({ runs: [{ ...run(5, 60), progress: { steps: "12" } }] })).toBe(true);
    expect(isRunning({ runs: [{ ...run(5, 60), progress: "no report" }] })).toBe(true);
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

// A run's newest progress report, in the shape the endpoint serves it, phase included.
const REPORT = { phase: "build", steps: 14, lines: 128, age_seconds: 126 };

describe("progressWords", () => {
  it("reads the counts and the age in the app's ago form", () => {
    expect(progressWords(REPORT, NOW)).toEqual({
      lead: "Step 14 · 128 lines",
      age: "2m ago",
      stale: false,
    });
  });

  it("uses the singular for one line and reads under a minute as just now", () => {
    expect(progressWords({ ...REPORT, steps: 1, lines: 1, age_seconds: 30 }, NOW)).toEqual({
      lead: "Step 1 · 1 line",
      age: "just now",
      stale: false,
    });
  });

  it("reads exactly a minute as 1m ago", () => {
    expect(progressWords({ ...REPORT, age_seconds: 60 }, NOW).age).toBe("1m ago");
  });

  it("keeps the ago form up to six minutes, then reads no update for the age", () => {
    expect(progressWords({ ...REPORT, age_seconds: 360 }, NOW)).toEqual({
      lead: "Step 14 · 128 lines",
      age: "6m ago",
      stale: false,
    });
    expect(progressWords({ ...REPORT, age_seconds: 361 }, NOW)).toEqual({
      lead: "Step 14 · 128 lines",
      age: "no update for 6m",
      stale: true,
    });
    expect(progressWords({ ...REPORT, age_seconds: 540 }, NOW).age).toBe("no update for 9m");
  });

  it("reads a stale age an hour old in the ago form's hours", () => {
    expect(progressWords({ ...REPORT, age_seconds: 3720 }, NOW).age).toBe("no update for 1h");
  });

  it("reads no progress for a report that is absent, null or not an object", () => {
    const none = { lead: "No progress yet", age: "", stale: false };
    expect(progressWords(undefined, NOW)).toEqual(none);
    expect(progressWords(null, NOW)).toEqual(none);
    expect(progressWords("Step 14", NOW)).toEqual(none);
    expect(progressWords(14, NOW)).toEqual(none);
    expect(progressWords(true, NOW)).toEqual(none);
    expect(progressWords([14, 128, 126], NOW)).toEqual(none);
  });

  it("reads no progress for a report missing a count or carrying a bad one", () => {
    const none = { lead: "No progress yet", age: "", stale: false };
    expect(progressWords({}, NOW)).toEqual(none);
    expect(progressWords({ phase: "build", steps: 14, lines: 128 }, NOW)).toEqual(none);
    expect(progressWords({ ...REPORT, steps: "14" }, NOW)).toEqual(none);
    expect(progressWords({ ...REPORT, lines: -1 }, NOW)).toEqual(none);
    expect(progressWords({ ...REPORT, age_seconds: 12.5 }, NOW)).toEqual(none);
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
