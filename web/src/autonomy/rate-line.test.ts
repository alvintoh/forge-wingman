import { describe, expect, it } from "bun:test";
import { rateLine } from "./format";
import type { MergeState } from "./types";

// state is the endpoint's auto-merge answer with the fields a test does not set
// left as the healthy default: the switch on, the rate under the bar, nothing
// escaped.
function state(overrides: Partial<MergeState>): MergeState {
  return {
    switch_on: true,
    rate_allows: true,
    rate_value: "9.8%",
    limit: "20.0%",
    window: 10,
    effective: true,
    reason: "on",
    escaped_pr: 0,
    escaped_repo: "",
    escaped_by: 0,
    escaped_reason: "",
    escape_days: 7,
    ...overrides,
  };
}

describe("rateLine", () => {
  it("words a rate at or under the bar as allowing auto-merge", () => {
    expect(rateLine(state({}))).toBe(
      "on: the trailing rewrite rate is 9.8% over the last 10 PRs a human merged, at or under the 20% bar",
    );
  });

  it("allows a rate exactly at the bar", () => {
    expect(rateLine(state({ rate_value: "20.0%" }))).toContain("at or under the 20% bar");
  });

  it("words a rate above the bar as holding auto-merge off", () => {
    const line = rateLine(
      state({
        rate_allows: false,
        effective: false,
        reason: "rate-above-bar",
        rate_value: "25.0%",
      }),
    );
    expect(line).toBe(
      "off: the trailing rewrite rate is 25.0% over the last 10 PRs a human merged, above the 20% bar",
    );
  });

  it("says which rate could not be computed", () => {
    expect(
      rateLine(state({ rate_allows: false, effective: false, reason: "too-few-measured" })),
    ).toContain("fewer than 10 PRs a human merged are measured");
    expect(
      rateLine(state({ reason: "too-few-measured", window: 12, rate_allows: false })),
    ).toContain("fewer than 12 PRs a human merged are measured");
    expect(
      rateLine(state({ rate_allows: false, effective: false, reason: "no-agent-lines" })),
    ).toContain("the measured PRs carry no agent lines");
    expect(
      rateLine(state({ rate_allows: false, effective: false, reason: "evidence-unread" })),
    ).toContain("the measured merges could not be read");
  });

  it("names the escaped PR and how it escaped", () => {
    const reverted = rateLine(
      state({
        rate_allows: false,
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
    const fixed = rateLine(
      state({
        rate_allows: false,
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

  it("says why the switch or the bar was not read", () => {
    expect(rateLine(state({ switch_on: false, effective: false, reason: "switch-off" }))).toContain(
      "switched off by you",
    );
    expect(rateLine(state({ switch_on: false, effective: false, reason: "switch-unread" }))).toBe(
      "off: the auto-merge settings could not be read",
    );
    expect(rateLine(state({ switch_on: false, effective: false, reason: "bar-invalid" }))).toBe(
      "off: the stored bar is not a whole number from 1 to 100",
    );
  });
});
