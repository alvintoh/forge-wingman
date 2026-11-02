import { useCallback, useEffect, useRef } from "react";
import { ago } from "./ago";

// A return to the tab reads the page only once the newer of the read on screen and the last ask is
// older than this, so a failing endpoint is read once per floor, and the two signals of one
// return (visibility and focus) ask once.
const RETURN_FLOOR_MS = 30_000;

// The button's name, and its tooltip, in each of its two states.
const READ_LABEL = "Read the page again";
const READING_LABEL = "Reading the page again";

/** PageRefresh is the state of the page's reads that the Overview's read control draws. */
export type PageRefresh = {
  // The oldest read the page still draws, or 0 before the first read lands.
  readAt: number;
  // A re-read failed, and the page still draws the read before it.
  stale: boolean;
  // A read of the page is open.
  fetching: boolean;
  // The wall clock at this draw, for the age words.
  now: number;
  // Asks for a read of everything the page draws, in place.
  ask: () => void;
};

/** returnDue tells whether a return to the tab reads again: the newer of the read and the last ask is over the floor old. */
export function returnDue(readAt: number, askedAt: number, now: number): boolean {
  return readAt !== 0 && now - Math.max(readAt, askedAt) >= RETURN_FLOOR_MS;
}

/** buttonLabel is the read button's name and tooltip. */
export function buttonLabel(fetching: boolean): string {
  return fetching ? READING_LABEL : READ_LABEL;
}

/** updatedWords says how long ago the page last read. */
export function updatedWords(readAt: number, now: number): string {
  return `updated ${ago(readAt, now)}`;
}

/** failedWords says that a re-read failed and how old the read on screen is. */
export function failedWords(readAt: number, now: number): string {
  return `The page could not be refreshed; showing the read from ${ago(readAt, now)}.`;
}

type ReturnReadArgs = {
  readAt: number;
  fetching: boolean;
  // Reads everything the page draws again, in place.
  refresh: () => void;
};

/**
 * useReturnRead returns the ask that reads the page, and reads it too when the tab is returned to
 * past the floor. Nothing reads on a timer.
 */
export function useReturnRead({ readAt, fetching, refresh }: ReturnReadArgs): () => void {
  // When the page last asked for a read, whether it then succeeded or failed.
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
      if (returnDue(readAt, askedAt.current, Date.now())) {
        ask();
      }
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

  return ask;
}
