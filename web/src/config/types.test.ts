import { describe, expect, it } from "bun:test";
import { isPollIntervalView } from "./types";

describe("isPollIntervalView", () => {
  it("accepts 10 and 15 minutes and refuses any other interval", () => {
    expect(isPollIntervalView({ poll_minutes: 10 })).toBe(true);
    expect(isPollIntervalView({ poll_minutes: 15 })).toBe(true);
    expect(isPollIntervalView({ poll_minutes: 5 })).toBe(false);
    expect(isPollIntervalView({ poll_minutes: "10" })).toBe(false);
  });
});
