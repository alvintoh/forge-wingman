import type { ReactNode } from "react";
import { captionClass } from "../caption";
import {
  CONCURRENCY_DEFINITION,
  isConcurrency,
  LIMITS,
  peakReachedN,
  type Concurrency,
} from "./concurrency";
import { useRead, type Read } from "./use-read";

// The drawing's example answer, laid out unseen while the read is out so the card keeps the height
// of one with the peak at N and five reasons held.
const HELD_VIEW: Concurrency = {
  peak: 2,
  runs_at_once: 2,
  runs_per_repo: 5,
  review_wip: 5,
  platform_cap: 20,
  held: [
    { reason: "concurrency", count: 14 },
    { reason: "review-wip", count: 6 },
    { reason: "repo-busy", count: 3 },
    { reason: "blocked", count: 2 },
    { reason: "large-cap", count: 1 },
  ],
};

// A limit or held reason's row: the Configuration page's 26px pitch.
const ROW = "flex justify-between gap-4 text-label leading-6.5";

// The peak row: its line on the left, the figure on the right, on one baseline. A status line takes
// the label's place and the figure's box stays, unseen, so the line sits where the label would.
function PeakRow({ view, children }: { view: Concurrency | null; children: ReactNode }) {
  return (
    <div className="mt-2 flex items-baseline justify-between gap-3">
      {children}
      <p
        aria-hidden={view === null ? true : undefined}
        className={`shrink-0 font-mono text-title leading-7 font-semibold text-ink ${view === null ? "invisible" : ""}`}
      >
        {view === null ? "0" : view.peak}
      </p>
    </div>
  );
}

// The card under its head, for an answer, or for none while the read is out: then each limit's label
// is drawn with its value empty, and the held list keeps the example's height unseen.
function Body({ view }: { view: Concurrency | null }) {
  const shown = view ?? HELD_VIEW;
  const atN = peakReachedN(shown);
  return (
    <>
      <PeakRow view={view}>
        {view === null ? (
          <p role="status" className="min-w-0 text-label text-ink2">
            Reading the concurrency…
          </p>
        ) : (
          <p className="min-w-0 text-label font-semibold text-ink">Peak in flight, 30 days</p>
        )}
      </PeakRow>
      {atN && (
        <p
          aria-hidden={view === null ? true : undefined}
          className={`mt-0.5 text-cap leading-4 text-ink2 ${view === null ? "invisible" : ""}`}
        >
          The peak reached runs at once (N).
        </p>
      )}
      <dl className={`border-t border-hair pt-4 ${atN ? "mt-3.5" : "mt-3"}`}>
        {LIMITS.map((limit) => (
          <div key={limit.label} className={ROW}>
            <dt className="min-w-0 text-ink2">{limit.label}</dt>
            <dd className="min-w-[2ch] text-right font-semibold text-ink">
              {view === null ? "" : limit.value(view)}
            </dd>
          </div>
        ))}
      </dl>
      <div className="mt-2.5 border-t border-hair pt-3.25">
        <h3 id="concurrency-held" className={`${captionClass} leading-5 text-ink2`}>
          runs held, 30 days
        </h3>
        {shown.held.length === 0 ? (
          <p className="mt-0.75 text-label leading-6.5 text-ink2">No run was held.</p>
        ) : (
          <dl
            aria-labelledby="concurrency-held"
            aria-hidden={view === null ? true : undefined}
            className={`mt-0.75 ${view === null ? "invisible" : ""}`}
          >
            {shown.held.map((row) => (
              <div key={row.reason} className={ROW}>
                <dt className="min-w-0 font-mono text-ink">{row.reason}</dt>
                <dd className="font-semibold text-ink">{row.count}</dd>
              </div>
            ))}
          </dl>
        )}
      </div>
    </>
  );
}

/**
 * ConcurrencyCard is the Statistics card for running several at once: the most runs in flight at once
 * over the trailing 30 days beside each limit admission holds, then the runs held over the same days
 * by reason. Every figure is the dispatcher's own across all projects, so a project chosen on the page
 * leaves it as it is.
 */
export function ConcurrencyCard() {
  const read = useRead("/api/concurrency", isConcurrency, "concurrencyNotRead");
  return <ConcurrencyCardView read={read} />;
}

// ConcurrencyCardView draws the card for the read it is given.
export function ConcurrencyCardView({ read }: { read: Read<Concurrency> }) {
  const failed = read.status === "error";
  return (
    <section
      aria-labelledby="concurrency-title"
      aria-busy={read.status === "loading"}
      className={`card-frame pt-6 max-sm:-mx-2.5 ${failed ? "pb-4" : "pb-3.5"}`}
    >
      <h2 id="concurrency-title" className={`${captionClass} text-ink2`}>
        concurrency
      </h2>
      <p className="mt-2 text-cap leading-4 text-ink2">{CONCURRENCY_DEFINITION}</p>
      {failed && (
        <PeakRow view={null}>
          <p role="alert" className="min-w-0 text-label text-ink2">
            The concurrency could not be read.
          </p>
        </PeakRow>
      )}
      {!failed && <Body view={read.status === "ready" ? read.body : null} />}
    </section>
  );
}
