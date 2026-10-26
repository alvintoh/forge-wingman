import type { Read } from "../stats/use-read";
import { InfoButton } from "./info-button";
import type { ConfigView, RunsPerRepoView } from "./types";

type ConcurrencySectionProps = {
  config: Read<ConfigView>;
  runsPerRepo: Read<RunsPerRepoView>;
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
  {
    label: "Poll every",
    value: ({ view }) => `${view.concurrency.poll_minutes} minutes`,
  },
];

/**
 * ConcurrencySection shows the limits admission runs under, read-only: the page offers no control
 * for them. Runs per repo is the cap saved in the surface's settings, the dispatcher's default
 * while none is saved; the rest are the dispatcher's own.
 */
export function ConcurrencySection({ config, runsPerRepo }: ConcurrencySectionProps) {
  const limits =
    config.status === "ready" && runsPerRepo.status === "ready"
      ? { view: config.body, runsPerRepo: runsPerRepo.body.runs_per_repo }
      : null;
  const loading = config.status === "loading" || runsPerRepo.status === "loading";
  const failed = config.status === "error" || runsPerRepo.status === "error";
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
      {limits !== null && <p className="text-ink2">Read-only; set on the dispatcher.</p>}
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
        </dl>
      )}
    </section>
  );
}
