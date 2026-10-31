import { useEffect, useRef } from "react";
import { ago } from "./ago";
import { pillSetWidth } from "./button-style";
import { SmallPill } from "./pill-button";

// A return to the tab reads the page only once the read on screen is older than this: a tab that
// flips back, or a window focused just after a read, must not read again.
const RETURN_FLOOR_MS = 30_000;

// One return to the tab fires both signals below, a moment apart. A second ask this close to the
// first is that same return, so it asks nothing.
const ONE_RETURN_MS = 1_000;

// REFRESH and REFRESHING... share one width, so the control keeps its size while a read is open.
const PILL_WIDTH = pillSetWidth(["REFRESH", "REFRESHING..."]);

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
 * RefreshStrip is the Overview's read control: when the page last read, and the REFRESH pill that
 * reads it again. Coming back to the tab reads it too, once the read on screen is over 30 seconds
 * old. Nothing reads on a timer.
 */
export function RefreshStrip({ readAt, stale, fetching, refresh }: RefreshStripProps) {
  // When this strip last asked for a read, so the two signals of one return ask once.
  const askedAt = useRef(0);

  useEffect(() => {
    function returned() {
      const at = Date.now();
      if (
        fetching ||
        readAt === 0 ||
        at - readAt < RETURN_FLOOR_MS ||
        at - askedAt.current < ONE_RETURN_MS
      ) {
        return;
      }
      askedAt.current = at;
      refresh();
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
  }, [fetching, readAt, refresh]);

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
          {/* One line either way: the age, or the failed read that still stands. */}
          <p
            role="status"
            className={`text-right text-cap leading-4 ${stale ? "text-defer" : "text-muted"}`}
          >
            {stale
              ? `The page could not be refreshed; showing the read from ${ago(readAt, now)}.`
              : `updated ${ago(readAt, now)}`}
          </p>
          <SmallPill
            kind="secondary"
            width={PILL_WIDTH}
            label={fetching ? "REFRESHING..." : "REFRESH"}
            aria-disabled={fetching ? true : undefined}
            onClick={fetching ? undefined : refresh}
          />
        </>
      )}
    </div>
  );
}
