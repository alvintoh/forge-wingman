import { describe, expect, it } from "bun:test";
import { modelName, wholePercent } from "./format";

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
  it("names the OpenCode Go models plainly", () => {
    expect(modelName("opencode-go/deepseek-v4.1-flash")).toBe("DeepSeek V4.1 Flash");
    expect(modelName("opencode-go/muse-spark-1.3-contributor")).toBe("Muse Spark 1.3 Contributor");
  });

  it("marks a past run's Command Code model apart from the Go model of the same name", () => {
    expect(modelName("command-code/deepseek/deepseek-v4.1-flash")).toBe(
      "DeepSeek V4.1 Flash (Command Code)",
    );
    expect(modelName("command-code/meta/muse-spark-1.3-contributor")).toBe(
      "Muse Spark 1.3 Contributor (Command Code)",
    );
  });

  it("reads an unknown id as it is", () => {
    expect(modelName("vendor/new-model")).toBe("vendor/new-model");
  });
});
