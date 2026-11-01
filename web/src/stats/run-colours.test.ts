import { describe, expect, it } from "bun:test";
import { fillOf, RUN_COLOURS } from "./run-colours";

describe("fillOf", () => {
  it("draws every kind the endpoint sends in its own colour", () => {
    for (const colour of RUN_COLOURS) {
      for (const kind of colour.kinds) {
        expect(fillOf(kind.kind)).toBe(colour.fill);
      }
    }
  });

  it("draws a kind the table does not hold in the muted colour", () => {
    expect(fillOf("a-kind-added-later")).toBe("bg-muted");
  });
});
