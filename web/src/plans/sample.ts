import type { HourRange, Plan, PlansView, PeakPrice, PricedModel } from "./view";
import { UNCONFIRMED } from "./view";

/** A priced model with the rates a test gives it, and neither a peak price nor a long-context one. */
export function pricedModel(
  model: string,
  input = 0.1,
  output = 0.5,
  cacheRead = 0.01,
): PricedModel {
  return {
    model,
    input_usd_per_mtok: input,
    output_usd_per_mtok: output,
    cache_read_usd_per_mtok: cacheRead,
    peak: null,
    above: null,
  };
}

/** A peak price of `multiplier` times the base rates of pricedModel, open over `hours`. */
export function peakOf(
  multiplier: number,
  hours: HourRange[] = [{ from: 1, to: 4 }],
  input = 0.2,
  output = 1,
  cacheRead = 0.02,
): PeakPrice {
  return {
    multiplier,
    hours,
    input_usd_per_mtok: input,
    output_usd_per_mtok: output,
    cache_read_usd_per_mtok: cacheRead,
  };
}

/** A plan with nothing confirmed, which a test fills with the fields it is about. */
export function planOf(id: string, overrides: Partial<Plan> = {}): Plan {
  return {
    plan: id,
    models: [],
    monthly_price_micros: UNCONFIRMED,
    requests_per_dollar: UNCONFIRMED,
    five_hour: UNCONFIRMED,
    weekly: UNCONFIRMED,
    monthly: UNCONFIRMED,
    strongest: UNCONFIRMED,
    strongest_cap_micros: UNCONFIRMED,
    features: [],
    harnesses: [],
    verdict: UNCONFIRMED,
    ...overrides,
  };
}

/** An answer holding `plans` and the three stages' requests. */
export function viewOf(plans: Plan[], overrides: Partial<PlansView> = {}): PlansView {
  return {
    plans,
    shapes: [
      { stage: "plan", runs: 7, shape: { fresh: 52000, cached: 2200, output: 1500 } },
      { stage: "build", runs: 4, shape: { fresh: 900, cached: 60000, output: 800 } },
      { stage: "review", runs: 5, shape: { fresh: 13000, cached: 0, output: 1200 } },
    ],
    model: null,
    stage: null,
    ...overrides,
  };
}
