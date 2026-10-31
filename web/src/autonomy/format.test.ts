import { describe, expect, it } from "bun:test";
import { wholePercent } from "./format";

describe("wholePercent", () => {
  it("rounds a tie to the even percent, as the endpoint's strings do", () => {
    expect(wholePercent(0.625)).toBe("62%");
    expect(wholePercent(0.375)).toBe("38%");
    expect(wholePercent(0.005)).toBe("0%");
    expect(wholePercent(0.035)).toBe("4%");
  });

  it("rounds a share past the tie up", () => {
    expect(wholePercent(0.6250001)).toBe("63%");
    expect(wholePercent(0.626)).toBe("63%");
  });

  it("prints an ordinary share as its whole percentage", () => {
    expect(wholePercent(0.62)).toBe("62%");
    expect(wholePercent(1)).toBe("100%");
  });
});
