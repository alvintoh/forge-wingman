import { describe, expect, it } from "bun:test";
import autonomy from "../../fixtures/autonomy.json";

describe("the Autonomy fixture", () => {
  it("names each PR's run by a ticket key, as the endpoint serves it", () => {
    const runs = autonomy.rewrite.prs.map((pr) => pr.run);
    expect(runs).toHaveLength(12);
    for (const run of runs) {
      expect(run).toMatch(/^[A-Z]+-\d+$/);
    }
  });

  it("reads the merge status window the endpoint serves", () => {
    // TrailingPRs in internal/dispatcher/rewrite.go; keep the two equal.
    expect(autonomy.auto_merge.window).toBe(10);
  });
});
