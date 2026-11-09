import { describe, expect, it } from "bun:test";
import { isInbox, stopReasonOf } from "./run";

const run = {
  run: "run-1",
  ticket: "FRG-52",
  repo: "forge-wingman",
  pr_url: "",
  wait_reason: "",
  updated_at: "2026-11-05T00:00:00Z",
  stopped: true,
};

describe("isInbox", () => {
  it("reads a stopped run's detail as text, and refuses one that is not", () => {
    expect(isInbox({ runs: [{ ...run, stop_detail: "Next model tier: a/b." }] })).toBe(true);
    expect(isInbox({ runs: [run] })).toBe(true);
    expect(isInbox({ runs: [{ ...run, stop_detail: { html: "<b>x</b>" } }] })).toBe(false);
  });
});

describe("stopReasonOf", () => {
  it("words a provider-setting stop", () => {
    expect(stopReasonOf({ ...run, stop_reason: "provider-setting" })).toBe(
      "Stopped, a provider setting refused the model",
    );
  });
});
