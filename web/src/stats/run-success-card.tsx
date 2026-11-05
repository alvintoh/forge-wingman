import type { ReactNode } from "react";
import { captionClass } from "../caption";
import { StatusMark } from "../status-mark";
import {
  describeRunSuccess,
  isRunSuccess,
  RUN_SUCCESS_DEFINITION,
  type RunSuccess,
  type RunSuccessState,
  type RunSuccessView,
} from "./run-success";
import { RunHistory } from "./run-history";
import { useRead, type Read } from "./use-read";

const WORD_COLOUR = {
  on: "text-ok",
  below: "text-defer",
  few: "text-ink",
} as const satisfies Record<RunSuccessState, string>;

// A status line sits level with the ready card's status word, so loading and failure hold its place.
const STATUS_LINE = "mt-5.25 text-label leading-4.5 text-ink2";

// The ready card's below-target body, drawn unseen while the read is out so the card keeps its height.
const HELD_VIEW = describeRunSuccess({
  recent: { passed: 24, runs: 30, rate: 24 / 30 },
  previous: { passed: 27, runs: 30, rate: 27 / 30 },
  failure: { class: "agent-timeout", count: 4 },
});

// The word keeps its place without the figure's line, which sets the row's baseline, by taking its
// margin; the mark sits 3px down so its centre is 6px above that baseline.
function ReadyBody({ view }: { view: RunSuccessView }) {
  return (
    <>
      <div
        className={`flex items-baseline justify-between gap-3 ${view.figure === null ? "mt-3.5" : "mt-1"}`}
      >
        <div className="flex min-w-0 items-baseline gap-2">
          <span className="relative top-0.75 inline-flex shrink-0">
            <StatusMark kind={view.state} />
          </span>
          <p className={`min-w-0 text-title leading-6 font-semibold ${WORD_COLOUR[view.state]}`}>
            {view.word}
          </p>
        </div>
        {view.figure !== null && (
          <p
            className={`shrink-0 font-mono text-hero leading-none font-semibold ${WORD_COLOUR[view.state]}`}
          >
            {view.figure}
          </p>
        )}
      </div>
      {view.target !== null && (
        <p className="mt-1.25 text-right text-label leading-3.5 text-ink2">{view.target}</p>
      )}
      <p className={`text-label leading-4.5 text-ink2 ${view.target === null ? "mt-1.75" : ""}`}>
        {view.rate}
      </p>
      {view.details.map((line) => (
        <p key={line} className="mt-1 text-label leading-4.5 text-ink2">
          {line}
        </p>
      ))}
    </>
  );
}

/**
 * RunSuccessCard is the Statistics card for run success: the last 30 counted runs as a shape, a word
 * and a figure against the target, the 30 runs before them and the most common stop, then the last
 * settled runs as its closing section. The endpoint is pooled, so a project chosen on the page leaves
 * the figures as All draws them, while the runs section follows the project.
 */
export function RunSuccessCard({ project }: { project: string | null }) {
  const read = useRead("/api/run-success", isRunSuccess, "runSuccessNotRead");
  return (
    <RunSuccessCardView read={read}>
      <RunHistory project={project} />
    </RunSuccessCardView>
  );
}

// RunSuccessCardView draws the card for the read it is given, with the children as its last section:
// they read for themselves, so the card's state never decides theirs.
export function RunSuccessCardView({
  read,
  children,
}: {
  read: Read<RunSuccess>;
  children: ReactNode;
}) {
  return (
    <section
      aria-labelledby="run-success-title"
      aria-busy={read.status === "loading"}
      className="card-frame pt-6 pb-6.5 max-sm:-mx-2.5"
    >
      <h2 id="run-success-title" className={`${captionClass} text-ink2`}>
        run success
      </h2>
      <p className="mt-2 text-cap leading-4 text-ink2">{RUN_SUCCESS_DEFINITION}</p>
      {read.status === "loading" && (
        <div className="grid">
          <div aria-hidden="true" className="invisible col-start-1 row-start-1">
            <ReadyBody view={HELD_VIEW} />
          </div>
          <p role="status" className={`col-start-1 row-start-1 self-start ${STATUS_LINE}`}>
            Reading the run success…
          </p>
        </div>
      )}
      {read.status === "error" && (
        <p role="alert" className={STATUS_LINE}>
          The run success could not be read.
        </p>
      )}
      {read.status === "ready" && <ReadyBody view={describeRunSuccess(read.body)} />}
      {children}
    </section>
  );
}
