import { useMedia } from "./use-media";

// The width a paged table switches page size at, and the rows it shows either side of it: one
// named constant per breakpoint, so a page pages by these and never by a literal.
export const PAGER_BREAKPOINT = 768;
export const PAGE_SIZE_WIDE = 10;
export const PAGE_SIZE_NARROW = 5;

/** usePageSize is how many rows a page holds now: ten from 768, five below it. */
export function usePageSize() {
  return useMedia(`(min-width: ${PAGER_BREAKPOINT}px)`) ? PAGE_SIZE_WIDE : PAGE_SIZE_NARROW;
}
