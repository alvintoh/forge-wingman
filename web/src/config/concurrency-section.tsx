import type { Read } from "../stats/use-read";
import { InfoButton } from "./info-button";
import { PollEvery } from "./poll-every";
import type { ConfigView, PollIntervalView, RunsPerRepoView } from "./types";

const POLL_LABEL_ID = "config-poll-every";

type ConcurrencySectionProps = {
  config: Read<ConfigView>;
  runsPerRepo: Read<RunsPerRepoView>;
  pollInterval: Read<PollIntervalView>;
  // The minutes the Poll every control shows, null until the interval is read.
  pollMinutes: number | null;
  disabled: boolean;
  onPollChoose: (minutes: number) => void;
};

// The figures a row shows, once both reads have answered.
type Limits = {
  view: ConfigView;
  runsPerRepo: number;
};

type Row = {
  label: string;
  value: (limits: Limits) => string;
  note?: string;
  about?: string;
};

const ROWS: readonly Row[] = [
  {
    label: "Review WIP",
    value: ({ view }) => `${view.concurrency.review_wip}`,
    note: "open agent PRs before new runs wait",
  },
  {
    label: "Runs per repo",
    value: ({ runsPerRepo }) => `${runsPerRepo}`,
    about:
      "Up to this many runs in one repository at once; runs whose planned files overlap never run together.",
  },
  {
    label: "GitHub job limit",
    value: ({ view }) => `${view.concurrency.platform_cap}`,
    note: "shared with CI",
  },
];

/**
 * ConcurrencySection shows the limits admission runs under. Poll every is the owner's, a choice of
 * 10 or 15 minutes the page's SAVE saves; the rest are read-only. Runs per repo is the cap saved in
 * the surface's settings, the dispatcher's default while none is saved; the others are the
 * dispatcher's own.
 */
export function ConcurrencySection({
  config,
  runsPerRepo,
  pollInterval,
  pollMinutes,
  disabled,
  onPollChoose,
}: ConcurrencySectionProps) {
  const reads = [config, runsPerRepo, pollInterval];
  const limits =
    config.status === "ready" && runsPerRepo.status === "ready"
      ? { view: config.body, runsPerRepo: runsPerRepo.body.runs_per_repo }
      : null;
  const failed = reads.some((read) => read.status === "error");
  // A failed read is the answer, even while another is still pending.
  const loading = !failed && reads.some((read) => read.status === "loading");
  return (
    <section
      aria-labelledby="config-concurrency"
      aria-busy={loading}
      className="border-t border-hair pt-3.5 pb-5 text-label leading-5"
    >
      <h2
        id="config-concurrency"
        className="flex min-h-11 items-center text-body font-semibold text-ink"
      >
        Concurrency
      </h2>
      {loading && (
        <p role="status" className="text-ink2">
          Reading the concurrency…
        </p>
      )}
      {failed && (
        <p role="alert" className="text-ink2">
          The concurrency could not be read.
        </p>
      )}
      {!failed && !loading && (
        <p className="text-ink2">Set on the dispatcher, except Poll every.</p>
      )}
      {!failed && (
        <dl className="mt-1.5 space-y-1.5">
          {ROWS.map((row) => (
            <div key={row.label} className="relative grid grid-cols-[minmax(0,1fr)_auto] gap-x-4">
              <dt className="text-ink2">{row.label}</dt>
              {/* A value's place is held while it loads. */}
              <dd className="min-w-[2ch] text-right font-semibold text-ink">
                {limits === null ? "" : row.value(limits)}
                {row.about !== undefined && (
                  <InfoButton name={`About ${row.label.toLowerCase()}`} text={row.about} />
                )}
              </dd>
              {row.note !== undefined && <dd className="col-span-2 text-muted">{row.note}</dd>}
            </div>
          ))}
          {/* The control wraps under its label where the row cannot hold both. */}
          <div className="flex flex-wrap items-center justify-between gap-x-4">
            <dt id={POLL_LABEL_ID} className="text-ink2">
              Poll every
            </dt>
            <dd className="ml-auto">
              <PollEvery
                labelledBy={POLL_LABEL_ID}
                value={pollMinutes}
                disabled={disabled}
                onChoose={onPollChoose}
              />
            </dd>
          </div>
        </dl>
      )}
    </section>
  );
}
