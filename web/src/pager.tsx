import { ActionButton } from "./action-button";
import { pageWindow } from "./page-window";

type PagerProps = {
  // What the range counts, as the page names it: "PRs" gives "PRs 1-10 of 34".
  noun: string;
  total: number;
  // The page on screen, counted from 0.
  page: number;
  pageSize: number;
  onPage: (page: number) => void;
};

/**
 * Pager moves a paged table between its pages: the range of rows on screen and the 44px PREVIOUS
 * and NEXT buttons, the words kept while a button is disabled. It draws nothing when every row
 * fits on one page.
 */
export function Pager({ noun, total, page, pageSize, onPage }: PagerProps) {
  const range = pageWindow(total, page, pageSize);
  if (!range.paged) {
    return null;
  }
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
      <p className="text-label text-ink2">
        {noun} {range.from}-{range.to} of {total}
      </p>
      <div className="flex items-center gap-2">
        <ActionButton
          label="PREVIOUS"
          kind="secondary"
          disabled={!range.previous}
          onClick={() => onPage(range.page - 1)}
        />
        <ActionButton
          label="NEXT"
          kind="secondary"
          disabled={!range.next}
          onClick={() => onPage(range.page + 1)}
        />
      </div>
    </div>
  );
}
