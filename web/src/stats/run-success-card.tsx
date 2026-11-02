import { captionClass } from "../caption";
import { StatusMark } from "../status-mark";
import {
  describeRunSuccess,
  isRunSuccess,
  RUN_SUCCESS_DEFINITION,
  type RunSuccessState,
  type RunSuccessView,
} from "./run-success";
import { useRead } from "./use-read";

const WORD_COLOUR = {
  on: "text-ok",
  below: "text-defer",
  few: "text-ink",
} as const satisfies Record<RunSuccessState, string>;

// A status line sits level with the ready card's status word, so loading and failure hold its place.
const STATUS_LINE = "mt-5.25 text-label leading-4.5 text-ink2";

// The ready card's below-target body, drawn unseen while the read is out so the card keeps its height.
const HELD_VIEW = describeRunSuccess({
  recent: { passed: 55, runs: 64, rate: 55 / 64 },
  previous: { passed: 53, runs: 58, rate: 53 / 58 },
  failure: { class: "agent-timeout", count: 4 },
});

// The word keeps its place without the figure's line, which sets the row's baseline, by taking its
// margin; the mark sits 3px down so its centre is 6px above that baseline.
function ReadyBody({ view, projectChosen }: { view: RunSuccessView; projectChosen: boolean }) {
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
      {projectChosen && (
        <p className="mt-2 text-label leading-4.5 font-semibold text-ink">
          run success: all projects
        </p>
      )}
    </>
  );
}

/**
 * RunSuccessCard is the Statistics card for run success: the trailing four weeks as a shape, a word
 * and a figure against the target, the previous four weeks and the most common stop. The endpoint is
 * pooled, so a project chosen on the page leaves the figures as All draws them and the card says so.
 */
export function RunSuccessCard({ projectChosen }: { projectChosen: boolean }) {
  const read = useRead("/api/run-success", isRunSuccess, "runSuccessNotRead");
  return (
    <section
      aria-labelledby="run-success-title"
      aria-busy={read.status === "loading"}
      className="card-frame pt-6 pb-4 max-sm:-mx-2.5"
    >
      <h2 id="run-success-title" className={`${captionClass} text-ink2`}>
        run success
      </h2>
      <p className="mt-2 text-cap leading-4 text-ink2">{RUN_SUCCESS_DEFINITION}</p>
      {read.status === "loading" && (
        <div className="grid">
          <div aria-hidden="true" className="invisible col-start-1 row-start-1">
            <ReadyBody view={HELD_VIEW} projectChosen={projectChosen} />
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
      {read.status === "ready" && (
        <ReadyBody view={describeRunSuccess(read.body)} projectChosen={projectChosen} />
      )}
    </section>
  );
}
