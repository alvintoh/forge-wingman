import { plansToScroll, scrolls, tableWidth } from "./overflow";
import type { Measure } from "./overflow";

/** What the region shows now: whether it scrolls, how many plans lie to the right, and the table's width once it scrolls. */
export type Seen = { scrolls: boolean; hidden: number; table: number | null };

/** measureOf reads the region's scroll position and width and the label column's width. */
export function measureOf(
  element: Pick<HTMLElement, "scrollLeft" | "clientWidth">,
  label: Pick<HTMLElement, "offsetWidth"> | null,
  plans: number,
): Measure {
  return {
    scrollLeft: element.scrollLeft,
    clientWidth: element.clientWidth,
    label: label?.offsetWidth ?? 0,
    plans,
  };
}

/** seenFrom is what the measures say the region shows. */
export function seenFrom(measure: Measure): Seen {
  const scrolling = scrolls(measure);
  return {
    scrolls: scrolling,
    hidden: plansToScroll(measure),
    table: scrolling ? tableWidth(measure) : null,
  };
}

/** What watchRegion needs of a resize observer: the browser's ResizeObserver is one, a test's stand-in another. */
type ObserverOf<E> = new (callback: () => void) => {
  observe: (target: E) => void;
  disconnect: () => void;
};

/**
 * watchRegion reads once, then again on every scroll of the element and whenever the observer
 * says it was resized, and returns what stops it.
 */
export function watchRegion<
  E extends Pick<HTMLElement, "addEventListener" | "removeEventListener">,
>(element: E, Observer: ObserverOf<E>, read: () => void): () => void {
  read();
  element.addEventListener("scroll", read, { passive: true });
  const observer = new Observer(read);
  observer.observe(element);
  return () => {
    element.removeEventListener("scroll", read);
    observer.disconnect();
  };
}
