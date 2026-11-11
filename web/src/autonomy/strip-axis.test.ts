import { describe, expect, it } from "bun:test";
import autonomy from "../../fixtures/autonomy.json";
import { buildAxis, mergeSlots } from "./axis";
import { isAutonomy } from "./guard";
import { STRIP_PRS } from "./pr-strip";

describe("the strip's own axis", () => {
  it("spreads its PRs over the strip's whole plot, however many merges the trend spans", () => {
    if (!isAutonomy(autonomy)) {
      throw new Error("the Autonomy fixture no longer matches the endpoint's shape");
    }
    const { prs, series } = autonomy.rewrite;
    const options = { width: 1280, padx: 26, gutter: 168, blank: 0.5, labels: 64 };
    const strip = buildAxis(mergeSlots([], prs.slice(0, STRIP_PRS)), options);
    const shared = buildAxis(mergeSlots(series, prs), { ...options, blank: 0, labels: undefined });

    const first = strip.xOfIndex(0);
    const last = strip.xOfIndex(strip.slots.length - 1);
    expect(first - strip.x0).toBeCloseTo(strip.step / 2);
    expect(strip.x1 - last).toBeCloseTo(strip.step / 2);

    // On the trend's axis the newest ten PRs sit right of its middle, as before.
    const [oldest] = strip.slots;
    const onShared = oldest && shared.xOfPR(oldest.repo, oldest.pr);
    expect(onShared).toBeGreaterThan(first);
  });
});
