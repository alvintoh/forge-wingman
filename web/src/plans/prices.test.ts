import { describe, expect, it } from "bun:test";
import { modelPrices, peakWindow, rate } from "./prices";
import { peakOf, planOf, pricedModel } from "./sample";
import type { PricedModel } from "./view";

const ABOVE = {
  context_tokens: 100000,
  input_usd_per_mtok: 0.5,
  output_usd_per_mtok: 2.5,
  cache_read_usd_per_mtok: 0.05,
};

function tiersOf(model: PricedModel) {
  const [priced] = modelPrices(planOf("paid", { models: [model] }));
  return priced?.tiers ?? [];
}

describe("rate", () => {
  it("prints three decimals, and 0.000 for a free price", () => {
    expect(rate(0.15)).toBe("0.150");
    expect(rate(0.003)).toBe("0.003");
    expect(rate(2.5)).toBe("2.500");
    expect(rate(0)).toBe("0.000");
  });
});

describe("peakWindow", () => {
  it("words the hours as weekdays in UTC, each range from its first hour to its close", () => {
    expect(
      peakWindow([
        { from: 1, to: 4 },
        { from: 6, to: 10 },
      ]),
    ).toBe("weekdays 01:00–04:00 and 06:00–10:00 UTC");
  });

  it("words one range alone, and three with commas before the last", () => {
    expect(peakWindow([{ from: 13, to: 24 }])).toBe("weekdays 13:00–24:00 UTC");
    expect(
      peakWindow([
        { from: 1, to: 2 },
        { from: 3, to: 4 },
        { from: 5, to: 6 },
      ]),
    ).toBe("weekdays 01:00–02:00, 03:00–04:00 and 05:00–06:00 UTC");
  });

  it("is null for a surcharge that names no hour", () => {
    expect(peakWindow([])).toBeNull();
  });
});

describe("modelPrices", () => {
  it("gives a model with no peak and no long-context price one row, any hour", () => {
    expect(tiersOf(pricedModel("paid/flat", 0.1, 0.2, 0.002))).toEqual([
      { label: "any hour", window: null, input: "0.100", output: "0.200", cacheRead: "0.002" },
    ]);
  });

  it("gives a model with a peak an off-peak row at the base rates, then a peak row with its window", () => {
    const model = {
      ...pricedModel("paid/zeta", 0.15, 0.6, 0.003),
      peak: peakOf(2, [{ from: 1, to: 4 }], 0.3, 1.2, 0.006),
    };
    expect(tiersOf(model)).toEqual([
      { label: "off-peak", window: null, input: "0.150", output: "0.600", cacheRead: "0.003" },
      {
        label: "peak, price ×2",
        window: "weekdays 01:00–04:00 UTC",
        input: "0.300",
        output: "1.200",
        cacheRead: "0.006",
      },
    ]);
  });

  it("says the multiplier the price carries, never a drawn 2", () => {
    const model = { ...pricedModel("paid/zeta"), peak: peakOf(1.5) };
    expect(tiersOf(model)[1]?.label).toBe("peak, price ×1.5");
  });

  it("gives a long-context price its own row, any hour above it still being the base", () => {
    const model = { ...pricedModel("paid/haiku", 0.1, 0.5, 0.01), above: ABOVE };
    expect(tiersOf(model)).toEqual([
      { label: "any hour", window: null, input: "0.100", output: "0.500", cacheRead: "0.010" },
      {
        label: "over 100,000 tokens of context",
        window: null,
        input: "0.500",
        output: "2.500",
        cacheRead: "0.050",
      },
    ]);
  });

  it("lists off-peak, peak and long-context rows, in that order, for a model with both", () => {
    const model = { ...pricedModel("paid/both"), peak: peakOf(2), above: ABOVE };
    expect(tiersOf(model).map((tier) => tier.label)).toEqual([
      "off-peak",
      "peak, price ×2",
      "over 100,000 tokens of context",
    ]);
  });

  it("prints no peak variant of the long-context row, since the server sends none", () => {
    const model = { ...pricedModel("paid/both"), peak: peakOf(2), above: ABOVE };
    expect(tiersOf(model).at(-1)).toEqual({
      label: "over 100,000 tokens of context",
      window: null,
      input: "0.500",
      output: "2.500",
      cacheRead: "0.050",
    });
  });

  it("names each model without its plan's prefix, in the server's order", () => {
    const plan = planOf("paid", { models: [pricedModel("paid/zeta"), pricedModel("paid/alpha")] });
    expect(modelPrices(plan).map((model) => model.name)).toEqual(["zeta", "alpha"]);
  });

  it("is empty for a plan with no models", () => {
    expect(modelPrices(planOf("bare"))).toEqual([]);
  });
});
