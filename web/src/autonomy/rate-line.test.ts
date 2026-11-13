import { describe, expect, it } from "bun:test";
import { rateLine } from "./format";
import type { MergeState } from "./types";

// state is the endpoint's auto-merge answer with the fields a test does not set
// left as the healthy default: the switch on, the rate under the guideline,
// nothing escaped.
function state(overrides: Partial<MergeState>): MergeState {
  return {
    switch_on: true,
    rate_value: "9.8%",
    guideline: "20.0%",
    window: 10,
    effective: true,
    reason: "on",
    rate_reason: "on",
    escaped_pr: 0,
    escaped_repo: "",
    escaped_by: 0,
    escaped_reason: "",
    escape_days: 7,
    ...overrides,
  };
}

describe("rateLine", () => {
  it("words a rate under the guideline as being at or under it, and says nothing of a hold", () => {
    const line = rateLine(state({}));
    expect(line).toBe(
      "on: the trailing rewrite rate is 9.8% over the last 10 PRs a human merged, at or under the 20% guideline",
    );
    expect(line).not.toContain("hold");
    expect(line).not.toContain("bar");
  });

  it("calls a rate exactly at the guideline at or under it", () => {
    expect(rateLine(state({ rate_value: "20.0%" }))).toContain("at or under the 20% guideline");
  });

  it("shows a rate above the guideline and holds nothing", () => {
    const line = rateLine(state({ reason: "on", rate_reason: "above-guideline", rate_value: "25.0%" }));
    expect(line).toBe(
      "on: the trailing rewrite rate is 25.0% over the last 10 PRs a human merged, above the 20% guideline",
    );
    expect(line).not.toContain("off");
  });

  it("keeps the rate beside a switched-off line", () => {
    expect(rateLine(state({ switch_on: false, effective: false, reason: "switch-off" }))).toBe(
      "off: switched off by you; the trailing rewrite rate is 9.8% over the last 10 PRs a human merged, at or under the 20% guideline",
    );
  });

  it("keeps the rate beside an unreadable switch", () => {
    expect(
      rateLine(state({ switch_on: false, effective: false, reason: "switch-unread" })),
    ).toBe(
      "off: the auto-merge settings could not be read; the trailing rewrite rate is 9.8% over the last 10 PRs a human merged, at or under the 20% guideline",
    );
  });

  it("says which rate could not be computed", () => {
    expect(
      rateLine(state({ rate_reason: "too-few-measured" })),
    ).toContain("the rate cannot be computed: fewer than 10 PRs a human merged are measured");
    expect(
      rateLine(state({ rate_reason: "too-few-measured", window: 12 })),
    ).toContain("fewer than 12 PRs a human merged are measured");
    expect(rateLine(state({ rate_reason: "no-agent-lines" }))).toContain(
      "the measured PRs carry no agent lines",
    );
    expect(rateLine(state({ rate_reason: "evidence-unread" }))).toContain(
      "the measured merges could not be read",
    );
  });

  it("names the escaped PR and how it escaped", () => {
    const reverted = rateLine(
      state({
        effective: false,
        reason: "escaped",
        escaped_pr: 341,
        escaped_repo: "octo/scratch",
        escaped_by: 350,
        escaped_reason: "reverted",
      }),
    );
    expect(reverted).toContain("octo/scratch#341 was reverted within 7 days of merging");
    expect(
      rateLine(
        state({
          reason: "escaped",
          escaped_pr: 341,
          escaped_repo: "octo/scratch",
          escaped_reason: "reverted",
          escape_days: 14,
        }),
      ),
    ).toContain("within 14 days of merging");
    expect(reverted).toContain("until you switch it on again");
    expect(reverted).toContain("at or under the 20% guideline");
    const fixed = rateLine(
      state({
        effective: false,
        reason: "escaped",
        escaped_pr: 344,
        escaped_repo: "octo/scratch",
        escaped_by: 350,
        escaped_reason: "fixed",
      }),
    );
    expect(fixed).toContain("octo/scratch#344 was named as fixed by #350");
  });
});
