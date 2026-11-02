import { describe as suite, expect, it } from "bun:test";
import type { PhaseSnapshot, Project } from "./project-list";
import { NOTHING_TO_START, describe } from "./project-view";

const NOW = Date.parse("2026-11-02T00:00:00Z");

function snapshot(over: Partial<PhaseSnapshot> = {}): PhaseSnapshot {
  return {
    phases: [{ name: "Phase 4", done: 71, total: 77 }],
    current: "Phase 4",
    in_flight: [],
    queued: 0,
    queued_blocked: 0,
    eligible: [],
    eligible_blocked: 0,
    unreadable: false,
    reason: "",
    at: "2026-11-01T23:00:00Z",
    ...over,
  };
}

function project(over: Partial<Project> = {}): Project {
  return {
    repository: "alvintoh/forge-wingman",
    linear_project: "Forge Wingman",
    added_at: "2026-10-01T00:00:00Z",
    paused: false,
    reach: { status: "reachable" },
    snapshot: snapshot(),
    queue: { in_flight: 0, held: 0 },
    ...over,
  };
}

suite("a project row whose phase has nothing to start", () => {
  it("shows START disabled, with the drawn reason", () => {
    const view = describe(project(), NOW);
    expect(view.startDisabled).toBe(true);
    expect(view.startable).toBe(false);
    expect(view.notice).toBe(
      "Cannot start as no ticket in this phase is ready to delegate. Please resolve them to start.",
    );
    expect(view.notice).toBe(NOTHING_TO_START);
  });

  it("is startable, with no notice, when a ticket is eligible", () => {
    const view = describe(project({ snapshot: snapshot({ eligible: ["FRG-1"] }) }), NOW);
    expect(view.startable).toBe(true);
    expect(view.startDisabled).toBe(false);
    expect(view.notice).toBeNull();
  });

  it("is unchanged while paused, running, queued or held", () => {
    for (const p of [
      project({ paused: true }),
      project({ queue: { in_flight: 1, held: 0 } }),
      project({ snapshot: snapshot({ queued: 2 }) }),
      project({ queue: { in_flight: 0, held: 1 } }),
      project({ snapshot: snapshot({ in_flight: [{ id: "FRG-2", title: "t", stage: "build" }] }) }),
    ]) {
      const view = describe(p, NOW);
      expect(view.startDisabled).toBe(false);
      expect(view.notice).toBeNull();
    }
  });

  it("is unchanged while checking, unreachable, unread, unreadable or without phases", () => {
    for (const p of [
      project({ reach: { status: "checking" } }),
      project({ reach: { status: "unreachable", reason: "not-found" } }),
      project({ snapshot: null }),
      project({ snapshot: snapshot({ unreadable: true }) }),
      project({ snapshot: snapshot({ phases: [], current: "" }) }),
    ]) {
      expect(describe(p, NOW).notice).not.toBe(NOTHING_TO_START);
    }
  });

  it("lets a pending start or a recorded refusal win the notice", () => {
    const pending = describe(project({ start_requested_at: "2026-11-02T00:00:00Z" }), NOW);
    expect(pending.notice).toBe("Start requested, delegates on the next poll.");
    const refused = describe(project({ start_refusal: "no-eligible-ticket" }), NOW);
    expect(refused.notice).toBe(
      "The last start was not carried out: no ticket was ready to start.",
    );
  });
});
