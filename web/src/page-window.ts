// PageWindow is the slice of a paged table on screen, and the moves it allows.
export type PageWindow = {
  // The page on screen, clamped to the last one so a narrower page size never leaves an empty
  // page under the rows.
  page: number;
  // The last page's number, counted from 0.
  last: number;
  // from and to are the first and last row numbers on the page, counted from 1, and 0 for a
  // table holding no rows.
  from: number;
  to: number;
  // paged is whether the table has more rows than one page, and so draws a pager at all.
  paged: boolean;
  previous: boolean;
  next: boolean;
};

/**
 * pageWindow maps a table's total, the page asked for and its page size to the slice to draw:
 * 34 rows on page 3 of ten rows is rows 31 to 34, with PREVIOUS allowed and NEXT not.
 */
export function pageWindow(total: number, page: number, pageSize: number): PageWindow {
  const last = Math.max(0, Math.ceil(total / pageSize) - 1);
  const current = Math.min(Math.max(0, page), last);
  return {
    page: current,
    last,
    from: total === 0 ? 0 : current * pageSize + 1,
    to: Math.min(total, (current + 1) * pageSize),
    paged: total > pageSize,
    previous: current > 0,
    next: current < last,
  };
}
