import { Fragment } from "react";
import { ActionButton } from "../action-button";
import { NOT_READ, READING } from "./view";

// The drawn skeleton lays out the comparison's table, so nothing jumps when the cards that hold it
// arrive: a label column of 120px on a phone or 176px, a header row of 60px and six rows of 56px.
// From a card inner width of 43.75rem (700px, every @[43.75rem] below) the table holds two plan
// columns, else one.
const ROWS = [0, 1, 2, 3, 4, 5];

/**
 * PlansLoading is the card shown while the plans are read: a sentence and the table's shape in
 * track bars, a skeleton and not a spinner. It is still, so reduced motion has nothing to stop.
 */
export function PlansLoading() {
  return (
    <section aria-busy="true" className="card-frame @container pt-6 pb-4 max-sm:-mx-2.5">
      {/* The page's live region says it, so a reader hears it once. */}
      <p aria-hidden="true" className="text-body leading-5 text-ink2">
        {READING}
      </p>
      <div
        aria-hidden="true"
        className="mt-1.75 grid grid-cols-[7.5rem_minmax(0,1fr)] @[43.75rem]:grid-cols-[11rem_repeat(2,minmax(0,1fr))]"
      >
        <span />
        <div className="h-15 pt-4">
          <span className="block h-4 w-[calc(100%-24px)] max-w-30 rounded-full bg-track" />
        </div>
        <div className="hidden h-15 pt-4 @[43.75rem]:block">
          <span className="block h-4 w-[calc(100%-24px)] max-w-30 rounded-full bg-track" />
        </div>
        {ROWS.map((row) => (
          <Fragment key={row}>
            <div className="h-14 pt-5">
              <span className="block h-3.5 w-[calc(100%-24px)] max-w-27.5 rounded-full bg-track" />
            </div>
            <div className="h-14 pt-5">
              <span className="block h-3.5 w-[calc(100%-24px)] max-w-40 rounded-full bg-track" />
            </div>
            <div className="hidden h-14 pt-5 @[43.75rem]:block">
              <span className="block h-3.5 w-[calc(100%-24px)] max-w-40 rounded-full bg-track" />
            </div>
          </Fragment>
        ))}
      </div>
    </section>
  );
}

type PlansErrorProps = {
  onRetry: () => void;
};

/** PlansError is the card shown when the plans could not be read: the one sentence and TRY AGAIN. */
export function PlansError({ onRetry }: PlansErrorProps) {
  return (
    <section className="card-frame pt-6 pb-5.5 max-sm:-mx-2.5">
      <p aria-hidden="true" className="text-head leading-6 text-ink">
        {NOT_READ}
      </p>
      <div className="mt-6">
        <ActionButton kind="primary" label="TRY AGAIN" onClick={onRetry} />
      </div>
    </section>
  );
}
