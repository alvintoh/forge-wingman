import { describe, expect, it } from "bun:test";
import { figuresOf, hasSplit, isComparison, type RunCache } from "./comparison";

// run is the endpoint's row with both phases measured; a case overrides what it
// varies.
const run = (over: Partial<RunCache> = {}): RunCache => ({
  run_id: "run-1",
  provider: "command-code",
  cached_share: 0.5,
  build_cache: "62% first, 58% phase",
  plan_cache: "41% first, 39% phase",
  build_first_share: 0.62,
  build_phase_share: 0.58,
  plan_first_share: 0.41,
  plan_phase_share: 0.39,
  ...over,
});

const noShares = {
  build_first_share: null,
  build_phase_share: null,
  plan_first_share: null,
  plan_phase_share: null,
};

describe("figuresOf", () => {
  it("takes a phase's two shares from the numbers, not the cell's text", () => {
    expect(figuresOf(run({ build_cache: "99% first, 99% phase" }), "build")).toEqual({
      first: 0.62,
      whole: 0.58,
      word: "",
    });
  });

  it("names the phase a run made no request in, for either phase", () => {
    const noPlan = run({ plan_cache: "none", plan_first_share: null, plan_phase_share: null });
    expect(figuresOf(noPlan, "plan").word).toBe("no plan stage");
    const noBuild = run({ build_cache: "none", build_first_share: null, build_phase_share: null });
    expect(figuresOf(noBuild, "build").word).toBe("no build stage");
  });

  it("keeps the endpoint's word where a phase recorded no share", () => {
    const notRecorded = run({
      plan_cache: "not recorded",
      plan_first_share: null,
      plan_phase_share: null,
    });
    expect(figuresOf(notRecorded, "plan").word).toBe("not recorded");
  });

  it("words a phase whose shares are not a pair, the endpoint sending both or neither", () => {
    const partial = run({ build_phase_share: null });
    expect(figuresOf(partial, "build")).toEqual({
      first: null,
      whole: null,
      word: "62% first, 58% phase",
    });
  });
});

describe("hasSplit", () => {
  it("holds a run with a phase's pair of shares", () => {
    const buildPair = { build_first_share: 0.62, build_phase_share: 0.58 };
    const planPair = { plan_first_share: 0.41, plan_phase_share: 0.39 };
    expect(hasSplit(run({ ...noShares, ...buildPair }))).toBe(true);
    expect(hasSplit(run({ ...noShares, ...planPair }))).toBe(true);
  });

  it("leaves a run with a lone share, or none, to collapse", () => {
    expect(hasSplit(run({ ...noShares, build_first_share: 0.62 }))).toBe(false);
    expect(hasSplit(run(noShares))).toBe(false);
  });
});

describe("isComparison", () => {
  it("accepts the endpoint's runs", () => {
    expect(isComparison({ runs: [run()] })).toBe(true);
  });

  it("rejects a run without its phase shares", () => {
    expect(isComparison({ runs: [{ run_id: "run-1", provider: "command-code" }] })).toBe(false);
  });
});
