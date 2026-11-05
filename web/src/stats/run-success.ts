import { isNumber, isString, nullable, shape } from "../autonomy/guard";

// The share of runs, in whole percent, the recent window is held to.
export const RUN_SUCCESS_TARGET_PERCENT = 90;

// The runs a window holds before a rate is stated; the endpoint serves no rate under it
// (internal/store/run_success.go).
export const RUN_SUCCESS_MIN_RUNS = 20;

export const RUN_SUCCESS_DEFINITION =
  "Run success is the share of runs that end in a PR with passing checks. Higher is better.";

type RunWindow = {
  passed: number;
  runs: number;
  // Null while the window holds fewer runs than a rate is stated over.
  rate: number | null;
};

type RunFailure = { class: string; count: number };

// The run success endpoint's answer: the failure is omitted when no run failed.
export type RunSuccess = {
  recent: RunWindow;
  previous: RunWindow;
  failure?: RunFailure;
};

const hasWindowFields = shape<RunWindow>({
  passed: isNumber,
  runs: isNumber,
  rate: nullable(isNumber),
});

// A window counts whole runs, passes no more than it counts, and states a rate only over some.
function isWindow(value: unknown): value is RunWindow {
  if (!hasWindowFields(value)) {
    return false;
  }
  const { passed, runs, rate } = value;
  if (!Number.isInteger(passed) || !Number.isInteger(runs) || passed < 0 || passed > runs) {
    return false;
  }
  return rate === null || (runs > 0 && rate >= 0 && rate <= 1);
}
const isFailure = shape<RunFailure>({ class: isString, count: isNumber });

/** isRunSuccess narrows the endpoint's answer to both windows and, where it is there, the failure. */
export function isRunSuccess(body: unknown): body is RunSuccess {
  if (typeof body !== "object" || body === null) {
    return false;
  }
  if (!("recent" in body) || !isWindow(body.recent)) {
    return false;
  }
  if (!("previous" in body) || !isWindow(body.previous)) {
    return false;
  }
  return !("failure" in body) || isFailure(body.failure);
}

export type RunSuccessState = "on" | "below" | "few";

export type RunSuccessView = {
  state: RunSuccessState;
  word: string;
  figure: string | null;
  target: string | null;
  rate: string;
  details: string[];
};

// A whole percent rounded down, so the figure never reads above the rate it states.
function floorPercent(passed: number, runs: number): string {
  return `${Math.floor((passed * 100) / runs)}%`;
}

function runsWord(runs: number): string {
  return runs === 1 ? "run" : "runs";
}

// The window before the recent one holds the runs before those, as many as it found.
function previousLine(previous: RunWindow): string {
  if (previous.runs === 0) {
    return "Previous runs: none";
  }
  const label = `Previous ${previous.runs} ${runsWord(previous.runs)}`;
  const counts = `(${previous.passed} of ${previous.runs})`;
  if (previous.rate === null) {
    return `${label}: too few for a rate ${counts}`;
  }
  return `${label}: ${floorPercent(previous.passed, previous.runs)} ${counts}`;
}

function failureLine(failure: RunFailure, failed: number): string {
  return `Top stop: ${failure.class.replaceAll("-", " ")}, ${failure.count} of ${failed} ${failed === 1 ? "failure" : "failures"}`;
}

/** describeRunSuccess words the endpoint's answer for the card, deciding on target from the raw rate. */
export function describeRunSuccess({ recent, previous, failure }: RunSuccess): RunSuccessView {
  if (recent.rate === null) {
    return {
      state: "few",
      word: "Too few runs",
      figure: null,
      target: null,
      rate: `Only ${recent.runs} ${runsWord(recent.runs)} so far; ${RUN_SUCCESS_MIN_RUNS} needed for a rate`,
      details: [],
    };
  }
  const onTarget = recent.passed * 100 >= RUN_SUCCESS_TARGET_PERCENT * recent.runs;
  const details = [previousLine(previous)];
  if (failure !== undefined) {
    details.push(failureLine(failure, recent.runs - recent.passed));
  }
  return {
    state: onTarget ? "on" : "below",
    word: onTarget ? "On target" : "Below target",
    figure: floorPercent(recent.passed, recent.runs),
    target: `target ${RUN_SUCCESS_TARGET_PERCENT}%`,
    rate: `Over the last ${recent.runs} runs (${recent.passed} of ${recent.runs})`,
    details,
  };
}
