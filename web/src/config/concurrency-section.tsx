import type { Read } from "../stats/use-read";
import { InfoButton } from "./info-button";
import type { ConfigView } from "./types";

type ConcurrencySectionProps = {
  config: Read<ConfigView>;
};

type Row = {
  label: string;
  value: (view: ConfigView) => string;
  note?: string;
  about?: string;
};

// Runs per repo has no setting of its own yet: one run at a time while a run's files are unknown.
const ROWS: readonly Row[] = [
  {
    label: "Review WIP",
    value: (view) => `${view.concurrency.review_wip}`,
    note: "open agent PRs before new runs wait",
  },
  {
    label: "Runs per repo",
    value: () => "1",
    about:
      "One run at a time while a run's files are unknown; two-stage runs with planned files may share.",
  },
  {
    label: "GitHub job limit",
    value: (view) => `${view.concurrency.platform_cap}`,
    note: "shared with CI",
  },
  {
    label: "Poll every",
    value: (view) => {
      const minutes = view.concurrency.poll_minutes;
      return `${minutes} minute${minutes === 1 ? "" : "s"}`;
    },
  },
];

/**
 * ConcurrencySection shows the limits the dispatcher runs under, read-only: they are set on the
 * dispatcher, so the page offers no control for them.
 */
export function ConcurrencySection({ config }: ConcurrencySectionProps) {
  const view = config.status === "ready" ? config.body : null;
  return (
    <section
      aria-labelledby="config-concurrency"
      aria-busy={config.status === "loading"}
      className="border-t border-hair pt-3.5 pb-5 text-label leading-5"
    >
      <h2
        id="config-concurrency"
        className="flex min-h-11 items-center text-body font-semibold text-ink"
      >
        Concurrency
      </h2>
      {config.status === "loading" && (
        <p role="status" className="text-ink2">
          Reading the concurrency…
        </p>
      )}
      {config.status === "error" && (
        <p role="alert" className="text-ink2">
          The concurrency could not be read.
        </p>
      )}
      {view !== null && <p className="text-ink2">Read-only; set on the dispatcher.</p>}
      {config.status !== "error" && (
        <dl className="mt-1.5 space-y-1.5">
          {ROWS.map((row) => (
            <div key={row.label} className="relative grid grid-cols-[minmax(0,1fr)_auto] gap-x-4">
              <dt className="text-ink2">{row.label}</dt>
              {/* A value's place is held while it loads. */}
              <dd className="min-w-[2ch] text-right font-semibold text-ink">
                {view === null ? "" : row.value(view)}
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
