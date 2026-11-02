import { describe, expect, it } from "bun:test";
import { displayName, type Project } from "./project-list";

function project(repository: string): Project {
  return {
    repository,
    linear_project: "P",
    added_at: "2026-10-01T00:00:00Z",
    paused: false,
    reach: { status: "reachable" },
    snapshot: null,
    queue: { in_flight: 0, held: 0 },
  };
}

describe("displayName", () => {
  it("drops the owner", () => {
    const all = [project("alvintoh/forge-wingman"), project("alvintoh/parallax")];
    expect(displayName("alvintoh/forge-wingman", all)).toBe("forge-wingman");
  });

  it("keeps the owner while another listed project shares the name", () => {
    const all = [project("alvintoh/widgets"), project("acme/Widgets")];
    expect(displayName("alvintoh/widgets", all)).toBe("alvintoh/widgets");
  });
});
