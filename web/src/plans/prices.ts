import { count, modelName } from "./view";
import type { HourRange, Plan, Rates } from "./view";

/** One price a model has: when it applies, and its three rates as the drawing prints them. */
export type PriceTier = {
  label: string;
  // The hours a peak price covers, else null.
  window: string | null;
  input: string;
  output: string;
  cacheRead: string;
};

/** A model's name under its plan's heading, and every price it has, the base one first. */
export type PriceModel = { name: string; tiers: PriceTier[] };

/** rate is a price in US dollars per million tokens to the three places the drawing prints. */
export function rate(usd: number): string {
  return usd.toFixed(3);
}

const hour = (at: number) => `${String(at).padStart(2, "0")}:00`;

/**
 * peakWindow words a surcharge's hours as the config means them: weekdays only, UTC, each range
 * from its first hour to the one it closes at. It is null where the surcharge names no hour.
 */
export function peakWindow(hours: readonly HourRange[]): string | null {
  const ranges = hours.map((range) => `${hour(range.from)}–${hour(range.to)}`);
  const last = ranges.pop();
  if (last === undefined) {
    return null;
  }
  const spoken = ranges.length === 0 ? last : `${ranges.join(", ")} and ${last}`;
  return `weekdays ${spoken} UTC`;
}

function tier(label: string, rates: Rates, window: string | null): PriceTier {
  return {
    label,
    window,
    input: rate(rates.input_usd_per_mtok),
    output: rate(rates.output_usd_per_mtok),
    cacheRead: rate(rates.cache_read_usd_per_mtok),
  };
}

/**
 * modelPrices is each of a plan's models with the prices it has, in the server's order: the base
 * rates ("off-peak" where a peak price follows, else "any hour"), then the peak price with its
 * window, then the price over a context length.
 */
export function modelPrices(plan: Plan): PriceModel[] {
  return plan.models.map((priced) => {
    const { peak, above } = priced;
    const tiers = [tier(peak === null ? "any hour" : "off-peak", priced, null)];
    if (peak !== null) {
      tiers.push(tier(`peak, price ×${peak.multiplier}`, peak, peakWindow(peak.hours)));
    }
    if (above !== null) {
      tiers.push(tier(`over ${count(above.context_tokens)} tokens of context`, above, null));
    }
    return { name: modelName(plan.plan, priced.model), tiers };
  });
}
