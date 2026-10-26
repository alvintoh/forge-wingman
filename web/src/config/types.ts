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

/** PeakView is what the page reads of the peak endpoint's answer: the mode runs wait under. */
export type PeakView = {
  mode: string;
};

type ConfigPhase = {
  phase: string;
  model: string;
};

type Concurrency = {
  review_wip: number;
  platform_cap: number;
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

/**
 * avoidsPeak is whether runs wait out their model's peak hours under mode: every mode but always
 * does, as the dispatcher holds a mode it cannot name like the default (readPeakMode).
 */
export function avoidsPeak(mode: string): boolean {
  return mode !== PEAK_MODE.always;
}

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
export const isPeakView = shape<PeakView>({ mode: isString });

/** isConfigView narrows the configuration endpoint's answer to every field the page reads. */
export const isConfigView = shape<ConfigView>({
  phases: listOf(shape<ConfigPhase>({ phase: isString, model: isString })),
  concurrency: shape<Concurrency>({
    review_wip: isNumber,
    platform_cap: isNumber,
    poll_minutes: isNumber,
  }),
  peak_windows: listOf(isPeakModel),
});

/**
 * peakText is a model's peak pricing in words: the hours it covers on weekdays (UTC) and what they
 * cost, the words for a model priced the same at every hour, or the words for one the plan prices
 * nowhere. Hours are whole hours, printed as a clock reads them. The price clause is joined with
 * no-break spaces, so a line that wraps carries it whole to the next line.
 */
export function peakText(model: PeakModel): string {
  if (model.peak === null) {
    return model.priced ? "Flat: the same price at every hour" : "Not priced by the plan";
  }
  const hours = model.peak.hours
    .map(({ from, to }) => [from, to].map((hour) => `${hour}:00`.padStart(5, "0")).join("–"))
    .join(", ");
  return `${hours} UTC on weekdays, ×${model.peak.multiplier}\u00a0the\u00a0price`;
}

/**
 * RunsPerRepoView is the runs-per-repo endpoint's answer: how many runs admission allows in
 * one repository at once, which the Config settings modal sets.
 */
export type RunsPerRepoView = {
  runs_per_repo: number;
};

/** isRunsPerRepoView narrows the runs-per-repo endpoint's answer. */
export const isRunsPerRepoView = shape<RunsPerRepoView>({ runs_per_repo: isNumber });

/** isAutoMergeAnswer narrows the auto-merge endpoint's answer to the switch it now holds. */
export const isAutoMergeAnswer = shape<{ enabled: boolean }>({ enabled: isBoolean });
