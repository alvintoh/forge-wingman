import { isString, listOf, shape } from "../autonomy/guard";

export const CONCURRENCY_DEFINITION =
  "Peak is the most runs in flight at once in the trailing 30 days, beside each limit.";

type HeldReason = { reason: string; count: number };

// The concurrency endpoint's answer: the held reasons come largest first, a reason with no hold left
// out, so an empty list means no run was held (cmd/surface/concurrency_stats.go).
export type Concurrency = {
  peak: number;
  runs_at_once: number;
  runs_per_repo: number;
  review_wip: number;
  platform_cap: number;
  held: HeldReason[];
};

const isCount = (value: unknown) => Number.isInteger(value) && (value as number) >= 0;
const isLimit = (value: unknown) => Number.isInteger(value) && (value as number) >= 1;

/** isConcurrency narrows the endpoint's answer to whole figures and held reasons that each count one or more. */
export const isConcurrency = shape<Concurrency>({
  peak: isCount,
  runs_at_once: isLimit,
  runs_per_repo: isLimit,
  review_wip: isLimit,
  platform_cap: isLimit,
  held: listOf(shape<HeldReason>({ reason: isString, count: isLimit })),
});

type Limit = { label: string; value: (view: Concurrency) => number };

// The limits the peak is read beside, in the order the card lists them.
export const LIMITS: readonly Limit[] = [
  { label: "Runs at once (N)", value: (view) => view.runs_at_once },
  { label: "Runs per repo", value: (view) => view.runs_per_repo },
  { label: "Review WIP", value: (view) => view.review_wip },
  { label: "GitHub job limit", value: (view) => view.platform_cap },
];

/** peakReachedN says whether the peak in flight met N, which the card states in words. */
export function peakReachedN(view: Concurrency): boolean {
  return view.peak >= view.runs_at_once;
}
