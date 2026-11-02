import { describe, expect, it } from "bun:test";
import fixture from "../../fixtures/comparison.json";
import { columnChars, finishedOf, isComparison, phaseCell, type RunCache } from "./comparison";

// run is the endpoint's row with both phases measured; a case overrides what it varies.
const run = (over: Partial<RunCache> = {}): RunCache => ({
  run_id: "FRG-101",
  provider: "command-code",
  cached_share: 0.5,
  build_cache: "62% first, 58% phase",
  plan_cache: "41% first, 39% phase",
  build_first_share: 0.62,
  plan_first_share: 0.41,
  settled_at: "2026-10-29T13:42:00Z",
  ...over,
});

describe("phaseCell", () => {
  it("is the first-request share, whatever the cell's text says", () => {
    expect(phaseCell(run({ build_cache: "99% first" }), "build")).toEqual({
      kind: "share",
      share: 0.62,
    });
    expect(phaseCell(run(), "plan")).toEqual({ kind: "share", share: 0.41 });
  });

  it("is a share for a phase that has only its first share", () => {
    expect(phaseCell(run({ plan_first_share: 0 }), "plan")).toEqual({ kind: "share", share: 0 });
  });

  it("is none for the phase the endpoint says made no request, for either phase", () => {
    expect(phaseCell(run({ plan_cache: "none", plan_first_share: null }), "plan")).toEqual({
      kind: "none",
    });
    expect(phaseCell(run({ build_cache: "none", build_first_share: null }), "build")).toEqual({
      kind: "none",
    });
  });

  it("is unrecorded for any other word with no share", () => {
    expect(phaseCell(run({ plan_cache: "not recorded", plan_first_share: null }), "plan")).toEqual({
      kind: "unrecorded",
    });
    expect(phaseCell(run({ build_first_share: null }), "build")).toEqual({ kind: "unrecorded" });
  });
});

describe("finishedOf", () => {
  it("words the settle time on Sydney's clock, whatever the viewer's", () => {
    expect(finishedOf(run({ settled_at: "2026-10-29T13:42:00Z" }))).toBe("30 Oct 2026, 00:42");
    expect(finishedOf(run({ settled_at: "2026-10-29T23:42:00Z" }))).toBe("30 Oct 2026, 10:42");
    expect(finishedOf(run({ settled_at: "2026-06-30T00:05:00Z" }))).toBe("30 Jun 2026, 10:05");
  });

  it("reads a run with no instant, or one that is no date, as not recorded", () => {
    expect(finishedOf(run({ settled_at: "0001-01-01T00:00:00Z" }))).toBeNull();
    expect(finishedOf(run({ settled_at: "" }))).toBeNull();
    expect(finishedOf(run({ settled_at: "garbage" }))).toBeNull();
  });
});

describe("isComparison", () => {
  it("accepts the endpoint's runs", () => {
    expect(isComparison({ runs: [run()] })).toBe(true);
  });

  it("accepts a run whose settle time is unreadable, which the table words as not recorded", () => {
    expect(isComparison({ runs: [run({ settled_at: "" })] })).toBe(true);
  });

  it("accepts a run whose phases recorded no shares", () => {
    expect(isComparison({ runs: [run({ build_first_share: null, plan_first_share: null })] })).toBe(
      true,
    );
  });

  it("rejects a run without its first-request shares", () => {
    const { build_first_share: _build, ...lacking } = run();
    expect(isComparison({ runs: [lacking] })).toBe(false);
  });

  it("rejects a run without its identity", () => {
    expect(isComparison({ runs: [{ run_id: "FRG-101", provider: "command-code" }] })).toBe(false);
  });
});

describe("columnChars", () => {
  it("is the longest run id, provider and finish text across every run", () => {
    const runs = [
      run({ run_id: "FRG-9", provider: "omp", settled_at: "2026-09-03T02:05:00Z" }),
      run({ run_id: "FRG-1000", provider: "command-code", settled_at: "2026-10-29T13:42:00Z" }),
      run({ run_id: "FRG-12", provider: "opencode", settled_at: "" }),
    ];
    expect(columnChars(runs)).toEqual({ runId: 8, provider: 12, finished: 18 });
  });

  it("counts a run with no readable finish as none and no runs as zero", () => {
    expect(columnChars([run({ settled_at: "" })]).finished).toBe(0);
    expect(columnChars([])).toEqual({ runId: 0, provider: 0, finished: 0 });
  });
});

describe("the comparison fixture", () => {
  it("is newest first, with the run that has no readable finish at the oldest end", () => {
    expect(isComparison(fixture)).toBe(true);
    const settled = fixture.runs.map((r) => Date.parse(r.settled_at));
    const readable = settled.filter((at) => new Date(at).getUTCFullYear() > 1);
    expect(readable).toEqual([...readable].sort((a, b) => b - a));
    expect(settled.slice(readable.length).every((at) => new Date(at).getUTCFullYear() <= 1)).toBe(
      true,
    );
    expect(readable.length).toBe(settled.length - 1);
  });
});
