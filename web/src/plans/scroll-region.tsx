import type { ReactNode, Ref } from "react";
import { cueWords } from "./overflow";

// The cue's id ties it to the region it describes; the page holds one wide table, so it is fixed.
const CUE_ID = "compare-card-cue";

type ScrollRegionProps = {
  regionRef: Ref<HTMLDivElement>;
  // Whether the table is wider than the region. Only then is the region a tab stop: a region with
  // nothing to scroll is a stop that does nothing.
  scrolls: boolean;
  // How many plans lie past the right edge; the cue shows while there are any.
  hidden: number;
  children: ReactNode;
};

/**
 * ScrollRegion holds a table that may be wider than its card and scrolls it sideways inside the
 * card, so the page never does. Once it scrolls it is a named region and one tab stop, and the
 * arrow keys move it while it has focus; Tab leaves it, since nothing in it takes focus. A scrolling
 * table keeps a 3rem gutter at its right, drawn as the cue's own: while plans lie past the edge the
 * gutter is covered in the card's fill, so none shows cut beside the cue, and the cue names how
 * many in words and a chevron. At the end of the scroll the cover goes and the gutter rests empty
 * after the last plan.
 */
export function ScrollRegion({ regionRef, scrolls, hidden, children }: ScrollRegionProps) {
  return (
    <div className="relative mt-3">
      <div
        ref={regionRef}
        {...(scrolls && {
          role: "region",
          "aria-label": "Plans table, scrolls sideways",
          "aria-describedby": hidden > 0 ? CUE_ID : undefined,
          tabIndex: 0,
        })}
        className="overflow-x-auto overscroll-x-contain"
      >
        <div className={scrolls ? "w-max pr-12" : undefined}>{children}</div>
      </div>
      {hidden > 0 && (
        <div className="pointer-events-none absolute inset-y-0 right-0 z-20 w-12 bg-card">
          <p
            id={CUE_ID}
            className="absolute top-4 right-2 flex h-5 items-center gap-1.5 text-cap font-semibold whitespace-nowrap text-ink2"
          >
            {cueWords(hidden)}
            <svg
              aria-hidden="true"
              width="6"
              height="10"
              viewBox="0 0 6 10"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.75"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <path d="M1 1l4 4l-4 4" />
            </svg>
          </p>
        </div>
      )}
    </div>
  );
}
