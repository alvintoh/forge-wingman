import { useCallback, useEffect, useRef } from "react";
import { ago } from "./ago";

// A return to the tab reads the page only once the newer of the read on screen and this strip's last
// ask is older than this, so a failing endpoint is read once per floor, and the two signals of one
// return (visibility and focus) ask once.
const RETURN_FLOOR_MS = 30_000;

// Lucide's rotate-cw (ISC) on its 24px grid, the two subpaths the arrow is drawn as.
const ROTATE_CW = "M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8M21 3v5h-5";

// The button's name, and its tooltip, in each of its two states.
const READ_LABEL = "Read the page again";
const READING_LABEL = "Reading the page again";

type RefreshStripProps = {
  // The oldest read the page still draws, or 0 before the first read lands.
  readAt: number;
  // A re-read failed, and the page still draws the read before it.
  stale: boolean;
  // A read of the page is open.
  fetching: boolean;
  // Reads everything the page draws again, in place.
  refresh: () => void;
};

/**
 * RefreshStrip is the Overview's read control: when the page last read, and the round outlined
 * button whose rotate-cw icon reads it again. Coming back to the tab reads it too, once the read on
 * screen is over 30 seconds old. Nothing reads on a timer.
 */
export function RefreshStrip({ readAt, stale, fetching, refresh }: RefreshStripProps) {
  // When this strip last asked for a read, whether it then succeeded or failed.
  const askedAt = useRef(0);

  // A press while a read is open does nothing: the button stays focusable, so the press gets here.
  const ask = useCallback(() => {
    if (fetching) {
      return;
    }
    askedAt.current = Date.now();
    refresh();
  }, [fetching, refresh]);

  useEffect(() => {
    function returned() {
      if (readAt === 0 || Date.now() - Math.max(readAt, askedAt.current) < RETURN_FLOOR_MS) {
        return;
      }
      ask();
    }
    function handleVisibility() {
      if (document.visibilityState === "visible") {
        returned();
      }
    }
    window.addEventListener("focus", returned);
    document.addEventListener("visibilitychange", handleVisibility);
    return () => {
      window.removeEventListener("focus", returned);
      document.removeEventListener("visibilitychange", handleVisibility);
    };
  }, [readAt, ask]);

  // The age is the wall clock read at this draw; the strip is neither memoised nor hydrated, so
  // there is nowhere else the instant could come from.
  // oxlint-disable-next-line react/purity
  const now = Date.now();

  return (
    // The band holds its 44px whether it draws the read or nothing, so the page never moves as the
    // first read lands.
    <div
      aria-busy={fetching ? true : undefined}
      className="flex min-h-11 items-center justify-end gap-3"
    >
      {readAt > 0 && (
        <>
          {/* One line either way: the age, or the failed read that still stands. Only the failure is
              announced; the status stays mounted so its text changing is what is heard. */}
          <div className="text-right text-cap leading-4">
            {!stale && <p className="text-muted">{`updated ${ago(readAt, now)}`}</p>}
            <p role="status" className="text-defer">
              {stale &&
                `The page could not be refreshed; showing the read from ${ago(readAt, now)}.`}
            </p>
          </div>
          <button
            type="button"
            aria-label={fetching ? READING_LABEL : READ_LABEL}
            title={fetching ? READING_LABEL : READ_LABEL}
            aria-disabled={fetching ? true : undefined}
            onClick={ask}
            className="group inline-flex size-11 shrink-0 cursor-pointer items-center justify-center aria-disabled:cursor-not-allowed focus-visible:outline-none!"
          >
            <span className="grid size-7 place-items-center rounded-full border border-ink2 bg-card text-ink group-aria-disabled:border-hair group-aria-disabled:bg-track group-aria-disabled:text-muted group-focus-visible:outline-3 group-focus-visible:outline-offset-2 group-focus-visible:outline-accent">
              <svg
                aria-hidden="true"
                width="16"
                height="16"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.8"
                strokeLinecap="round"
                strokeLinejoin="round"
                className={`block ${fetching ? "animate-spin motion-reduce:animate-none" : ""}`}
              >
                <path d={ROTATE_CW} />
              </svg>
            </span>
          </button>
        </>
      )}
    </div>
  );
}
