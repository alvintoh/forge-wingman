import type { Read } from "../stats/use-read";
import { InfoButton } from "./info-button";
import type { ConfigView, RunsPerRepoView } from "./types";

type ConcurrencySectionProps = {
  config: Read<ConfigView>;
  runsPerRepo: Read<RunsPerRepoView>;
};

type Row = {
  label: string;
  // value is the row's figure, empty while its read is unknown.
  value: string;
  note?: string;
  about?: string;
};

/**
 * ConcurrencySection shows the limits admission runs under, read-only: the dispatcher's own are set
 * on it, and the runs-per-repo cap is the owner's, set in the inbox's Config settings.
 */
export function ConcurrencySection({ config, runsPerRepo }: ConcurrencySectionProps) {
  const view = config.status === "ready" ? config.body : null;
  const cap = runsPerRepo.status === "ready" ? runsPerRepo.body.runs_per_repo : null;
  const busy = config.status === "loading" || runsPerRepo.status === "loading";
  const failed = config.status === "error" || runsPerRepo.status === "error";
  const rows: readonly Row[] = [
    {
      label: "Review WIP",
      value: view === null ? "" : `${view.concurrency.review_wip}`,
      note: "open agent PRs before new runs wait",
    },
    {
      label: "Runs per repo",
      value: cap === null ? "" : `${cap}`,
      about: "Set in the inbox's Config settings. The dispatcher holds each repository to it.",
    },
    {
      label: "GitHub job limit",
      value: view === null ? "" : `${view.concurrency.platform_cap}`,
      note: "shared with CI",
    },
    {
      label: "Poll every",
      value: view === null ? "" : `${view.concurrency.poll_minutes} minutes`,
    },
  ];
  return (
    <section
      aria-labelledby="config-concurrency"
      aria-busy={busy}
      className="border-t border-hair pt-3.5 pb-5 text-label leading-5"
    >
      <h2
        id="config-concurrency"
        className="flex min-h-11 items-center text-body font-semibold text-ink"
      >
        Concurrency
      </h2>
      {busy && (
        <p role="status" className="text-ink2">
          Reading the concurrency…
        </p>
      )}
      {failed && (
        <p role="alert" className="text-ink2">
          Part of the concurrency could not be read.
        </p>
      )}
      {!busy && !failed && <p className="text-ink2">Read-only.</p>}
      {config.status !== "error" && (
        <dl className="mt-1.5 space-y-1.5">
          {rows.map((row) => (
            <div key={row.label} className="relative grid grid-cols-[minmax(0,1fr)_auto] gap-x-4">
              <dt className="text-ink2">{row.label}</dt>
              {/* A value's place is held while it loads. */}
              <dd className="min-w-[2ch] text-right font-semibold text-ink">
                {row.value}
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
