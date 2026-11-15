import { describe, expect, it } from "bun:test";
import { linesCaption, modelName, wholePercent } from "./format";
import type { Tile } from "./types";

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

describe("modelName", () => {
  it("names a model once whatever provider ran it", () => {
    expect(modelName("opencode-go/deepseek-v4.1-flash")).toBe("DeepSeek V4.1 Flash");
    expect(modelName("command-code/deepseek/deepseek-v4.1-flash")).toBe("DeepSeek V4.1 Flash");
    expect(modelName("deepseek-v4.1-flash")).toBe("DeepSeek V4.1 Flash");
  });

  it("leaves the contributor suffix out of a model's name", () => {
    expect(modelName("opencode-go/muse-spark-1.3-contributor")).toBe("Muse Spark 1.3");
    expect(modelName("command-code/meta/muse-spark-1.3-contributor")).toBe("Muse Spark 1.3");
  });

  it("reads an unknown id as it is", () => {
    expect(modelName("vendor/new-model")).toBe("vendor/new-model");
  });
});

describe("linesCaption", () => {
  const tile: Tile = {
    prs: [175],
    asked: 10,
    agent_lines: 7982,
    owner_lines: 3440,
    rate: "43.1%",
    rate_value: 0.431,
    previous_rate_value: null,
    trend: "flat",
  };

  it("states the owner lines of the agent lines, with thousands separators", () => {
    expect(linesCaption(tile)).toEqual({
      figures: "3,440 of 7,982",
      noun: "agent lines",
      text: "3,440 of 7,982 agent lines",
    });
  });

  it("states nothing for a window with no agent lines", () => {
    expect(linesCaption({ ...tile, agent_lines: 0, owner_lines: 4715 })).toBeNull();
  });
});
