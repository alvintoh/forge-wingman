import { describe, expect, it } from "bun:test";
import { projectName } from "./project-name";

describe("projectName", () => {
  it("drops the owner", () => {
    expect(projectName("alvintoh/forge-wingman")).toBe("forge-wingman");
  });

  it("keeps a name that has no owner", () => {
    expect(projectName("forge-wingman")).toBe("forge-wingman");
  });
});
