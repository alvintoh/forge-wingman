import { useLayoutEffect, useRef, useState } from "react";
import { measureOf, seenFrom, watchRegion } from "./watch-region";
import type { Seen } from "./watch-region";

/**
 * useHiddenPlans watches a scrolling table and says whether it scrolls at all, how many plans lie
 * past its right edge and how wide the table is to be once it scrolls. The region's ref goes on the
 * element that scrolls, and the label's on the table's first cell, the sticky column whose width
 * the plans do not share. It reads before the first paint, so a table that scrolls never shows
 * once without its region and cue, and again on every scroll and whenever the region is resized,
 * so a window dragged wider drops the cue once the plans fit.
 */
export function useHiddenPlans(plans: number) {
  const region = useRef<HTMLDivElement>(null);
  const label = useRef<HTMLTableCellElement>(null);
  const [seen, setSeen] = useState<Seen>({ scrolls: false, hidden: 0, table: null });

  useLayoutEffect(() => {
    const element = region.current;
    if (element === null) {
      return;
    }
    return watchRegion(element, ResizeObserver, () => {
      const next = seenFrom(measureOf(element, label.current, plans));
      // The same reading keeps the same object, so a scroll that changes nothing renders nothing.
      setSeen((now) =>
        now.scrolls === next.scrolls && now.hidden === next.hidden && now.table === next.table
          ? now
          : next,
      );
    });
  }, [plans]);

  return { region, label, ...seen };
}
