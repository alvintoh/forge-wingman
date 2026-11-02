import { describe, expect, it } from "bun:test";
import type { Inbox } from "./inbox";
import type { InboxRun } from "./inbox/run";
import { attentionCount } from "./inbox-context";

function runs(count: number): InboxRun[] {
  return Array.from({ length: count }, (_, i) => ({
    run: `run-${i}`,
    ticket: `T-${i}`,
    repo: "acme/widgets",
    pr_url: "",
    wait_reason: "draft",
    updated_at: "2026-03-01T12:00:00Z",
  }));
}

function state(status: Inbox["state"]["status"], count = 0, stale = false): Inbox["state"] {
  return { status, runs: runs(count), readAt: 0, stale };
}

describe("attentionCount", () => {
  it("counts the runs of a read that landed", () => {
    expect(attentionCount(state("ready", 3))).toBe(3);
  });

  it("is zero for a read that found nothing", () => {
    expect(attentionCount(state("ready"))).toBe(0);
  });

  it("keeps the last count while a re-read fails", () => {
    expect(attentionCount(state("ready", 2, true))).toBe(2);
  });

  it("has no count while the first read is open", () => {
    expect(attentionCount(state("loading"))).toBeNull();
  });

  it("has no count when no read ever landed", () => {
    expect(attentionCount(state("error"))).toBeNull();
  });
});
