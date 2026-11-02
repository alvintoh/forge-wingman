import { buttonLabel, failedWords, updatedWords, type PageRefresh } from "./refresh-read";

// Lucide's rotate-cw (ISC) on its 24px grid, the two subpaths the arrow is drawn as.
const ROTATE_CW = "M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8M21 3v5h-5";

/** RefreshButton is the round outlined button whose rotate-cw icon reads the page again. */
export function RefreshButton({ refresh }: { refresh: PageRefresh }) {
  const { fetching, ask } = refresh;
  return (
    <button
      type="button"
      aria-label={buttonLabel(fetching)}
      title={buttonLabel(fetching)}
      aria-disabled={fetching ? true : undefined}
      onClick={ask}
      className="group relative -mr-2 -my-[9px] inline-flex size-11 shrink-0 cursor-pointer items-center justify-center aria-disabled:cursor-not-allowed focus-visible:outline-none!"
    >
      <span className="grid size-7 place-items-center rounded-full border border-ink2 bg-card text-ink group-aria-disabled:border-hair group-aria-disabled:bg-track group-aria-disabled:text-muted group-focus-visible:outline-3 group-focus-visible:outline-offset-2 group-focus-visible:outline-accent">
        <svg
          aria-hidden="true"
          width="16"
          height="16"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2.7"
          strokeLinecap="round"
          strokeLinejoin="round"
          className={`block ${fetching ? "animate-spin motion-reduce:animate-none" : ""}`}
        >
          <path d={ROTATE_CW} />
        </svg>
      </span>
    </button>
  );
}

/** RefreshClock is the words for how long ago the page read, drawn while no failed re-read stands. */
export function RefreshClock({ refresh }: { refresh: PageRefresh }) {
  if (refresh.stale) {
    return null;
  }
  return (
    <p className="text-cap leading-4 text-muted">{updatedWords(refresh.readAt, refresh.now)}</p>
  );
}

/** RefreshNotice is the status line that says a re-read failed; it stays mounted so its text changing is what is heard. */
export function RefreshNotice({ refresh }: { refresh: PageRefresh }) {
  if (refresh.readAt === 0) {
    return null;
  }
  return (
    // The card below keeps its own 16px margin; -mb-1.5 pulls it up to the drawn 14px under the last baseline.
    <p
      role="status"
      className={`text-cap leading-4 text-defer ${refresh.stale ? "mt-[7px] -mb-1.5" : ""}`}
    >
      {refresh.stale && failedWords(refresh.readAt, refresh.now)}
    </p>
  );
}
