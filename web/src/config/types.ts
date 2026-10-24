import { isBoolean, isNumber, isString, listOf, nullable, shape } from "../autonomy/guard";

type PeakHour = {
  from: number;
  to: number;
};

type PeakWindow = {
  multiplier: number;
  // The weekday UTC hour ranges the multiplier applies in, each [from, to).
  hours: PeakHour[];
};

/**
 * PeakModel is one model's peak pricing as the peak and configuration endpoints serve it: null for
 * a model priced the same at every hour and for one its plan does not price, which priced tells
 * apart.
 */
export type PeakModel = {
  model: string;
  peak: PeakWindow | null;
  priced: boolean;
};

/** PeakView is the peak endpoint's answer; holding is whether a peak window is open now. */
export type PeakView = {
  mode: string;
  holding: boolean;
};

type ConfigPhase = {
  phase: string;
  model: string;
};

type Concurrency = {
  review_wip: number;
  platform_cap: number;
  two_stage: boolean;
  poll_minutes: number;
};

/** ConfigView is the configuration endpoint's answer. */
export type ConfigView = {
  phases: ConfigPhase[];
  concurrency: Concurrency;
  peak_windows: PeakModel[];
};

/**
 * The two modes a run waits out its model's peak-price hours under, as the endpoint names them
 * (internal/dispatcher/peak.go).
 */
export const PEAK_MODE = { offPeakOnly: "off-peak-only", always: "always" } as const;

const isPeakWindow = shape<PeakWindow>({
  multiplier: isNumber,
  hours: listOf(shape<PeakHour>({ from: isNumber, to: isNumber })),
});

const isPeakModel = shape<PeakModel>({
  model: isString,
  peak: nullable(isPeakWindow),
  priced: isBoolean,
});

/** isPeakView narrows the peak endpoint's answer to the fields the page reads. */
export const isPeakView = shape<PeakView>({ mode: isString, holding: isBoolean });

/** isConfigView narrows the configuration endpoint's answer to every field the page reads. */
export const isConfigView = shape<ConfigView>({
  phases: listOf(shape<ConfigPhase>({ phase: isString, model: isString })),
  concurrency: shape<Concurrency>({
    review_wip: isNumber,
    platform_cap: isNumber,
    two_stage: isBoolean,
    poll_minutes: isNumber,
  }),
  peak_windows: listOf(isPeakModel),
});

/**
 * peakText is a model's peak pricing in words: the hours it covers on weekdays (UTC) and what they
 * cost, the words for a model priced the same at every hour, or the words for one the plan prices
 * nowhere. Hours are whole hours, printed as a clock reads them.
 */
export function peakText(model: PeakModel): string {
  if (model.peak === null) {
    return model.priced ? "Flat: the same price at every hour" : "Not priced by the plan";
  }
  const hours = model.peak.hours
    .map(({ from, to }) => [from, to].map((hour) => `${hour}:00`.padStart(5, "0")).join("–"))
    .join(", ");
  return `${hours} UTC on weekdays, ×${model.peak.multiplier} the price`;
}
