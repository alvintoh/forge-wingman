import { useEffect, useRef } from "react";
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
  // Whether the range line announces a page change; where a table has two pagers, one does.
  announce?: boolean;
};

/**
 * Pager moves a paged table between its pages: the range of rows on screen and the 44px PREVIOUS
 * and NEXT buttons, the words kept while a button is disabled. The range line is a polite live
 * region, and a press that disables its own button moves focus to the other. It draws nothing
 * when every row fits on one page.
 */
export function Pager({ noun, total, page, pageSize, onPage, announce = true }: PagerProps) {
  const range = pageWindow(total, page, pageSize);
  const previous = useRef<HTMLButtonElement>(null);
  const next = useRef<HTMLButtonElement>(null);
  const pressed = useRef<"previous" | "next" | null>(null);
  useEffect(() => {
    const was = pressed.current;
    pressed.current = null;
    if (was === "next" && !range.next) {
      previous.current?.focus();
    } else if (was === "previous" && !range.previous) {
      next.current?.focus();
    }
  }, [range.page, range.previous, range.next]);
  if (!range.paged) {
    return null;
  }
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
      <p role={announce ? "status" : undefined} className="text-label text-ink2">
        {noun} {range.from}-{range.to} of {total}
      </p>
      <div className="flex items-center gap-2">
        <ActionButton
          ref={previous}
          label="PREVIOUS"
          kind="secondary"
          disabled={!range.previous}
          onClick={() => {
            pressed.current = "previous";
            onPage(range.page - 1);
          }}
        />
        <ActionButton
          ref={next}
          label="NEXT"
          kind="secondary"
          disabled={!range.next}
          onClick={() => {
            pressed.current = "next";
            onPage(range.page + 1);
          }}
        />
      </div>
    </div>
  );
}
