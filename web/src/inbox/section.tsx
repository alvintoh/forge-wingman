import { useEffect, useId, useRef, useState } from "react";
import { useInbox } from "../inbox-context";
import { TryAgainPill } from "../pill-button";
import { LIST_DIALOG_ATTR, type Opened, type Position, cellIn, positionOf } from "./keyboard";
import { MergeDialog } from "./merge-dialog";
import { InboxRow } from "./row";
import { RowSheet } from "./row-sheet";
import type { InboxRun } from "./run";

// InboxSection is the inbox's ACTIONS section: the PRs that wait on the owner. A row opens its
// sheet of actions; its MERGE PR opens its confirm and then shows the merge on the row.
//
// The list is one tab stop, the APG layout grid: the arrow keys move between the rows and from a row
// to its MERGE PR and back, Home and End to the first and last row, and the stop stays on the cell
// last focused.
export function InboxSection() {
  const { state, refresh, retry, rows, announcement, merge, clearRefusal } = useInbox();
  const [target, setTarget] = useState<Opened | null>(null);
  const [sheet, setSheet] = useState<Opened | null>(null);
  const [active, setActive] = useState<Position | null>(null);
  const headingId = useId();
  const headingRef = useRef<HTMLHeadingElement>(null);
  const listRef = useRef<HTMLUListElement>(null);
  // Whether the list holds the focus, or lent it to a dialog it opened.
  const holdsFocus = useRef(false);
  // The active row's place in the list as last rendered, for when it leaves.
  const activeIndex = useRef(0);

  // The list is read once for the whole app, so the Overview reads it again each time it opens,
  // unless that first read is still under way.
  const loadingAtOpen = useRef(state.status === "loading");
  useEffect(() => {
    if (!loadingAtOpen.current) {
      refresh();
    }
  }, [refresh]);

  const runs = state.status === "ready" ? state.runs : [];
  const activeAt = runs.findIndex((run) => run.run === active?.run);
  const first = runs[0];
  let stop: Position | null = null;
  if (active !== null && activeAt >= 0) {
    stop = active;
  } else if (first !== undefined) {
    stop = { run: first.run, cell: "row" };
  }

  // When the focused cell leaves (a merged row's pill, or the row on the re-read after it), the focus
  // goes to that row, else the row now in its place, else the one above, else the heading, never to
  // the page.
  useEffect(() => {
    if (activeAt >= 0) {
      activeIndex.current = activeAt;
    }
    if (state.status !== "ready" || !holdsFocus.current || active === null) {
      return;
    }
    const focused = document.activeElement;
    if (focused !== null && focused !== document.body) {
      return;
    }
    const run =
      activeAt >= 0 ? runs[activeAt] : runs[Math.min(activeIndex.current, runs.length - 1)];
    if (run === undefined) {
      holdsFocus.current = false;
      headingRef.current?.focus();
      return;
    }
    cellIn(listRef.current, run.run, "row")?.focus();
  });

  function handleFocus(event: React.FocusEvent<HTMLUListElement>) {
    const at = positionOf(event.target);
    if (at === null) {
      return;
    }
    holdsFocus.current = true;
    setActive((previous) =>
      previous?.run === at.run && previous.cell === at.cell ? previous : at,
    );
  }

  // Focus moving into a dialog the list opened comes back to it on close, so the list still holds
  // it. Focus lost with no new holder is the list's only while the cell that had it is gone.
  function handleBlur(event: React.FocusEvent<HTMLUListElement>) {
    const to = event.relatedTarget;
    if (
      to instanceof Element &&
      (event.currentTarget.contains(to) || to.closest(`[${LIST_DIALOG_ATTR}]`) !== null)
    ) {
      return;
    }
    if (to === null) {
      const left = event.target;
      requestAnimationFrame(() => {
        if (left.isConnected) {
          holdsFocus.current = false;
        }
      });
      return;
    }
    holdsFocus.current = false;
  }

  function handleKeyDown(event: React.KeyboardEvent<HTMLUListElement>) {
    const at = positionOf(event.target);
    if (at === null || event.altKey || event.ctrlKey || event.metaKey) {
      return;
    }
    const index = runs.findIndex((run) => run.run === at.run);
    let next: Position | null = null;
    const row = (i: number): Position | null => {
      const run = runs[i];
      return run === undefined ? null : { run: run.run, cell: "row" };
    };
    switch (event.key) {
      case "ArrowDown":
        next = row(index + 1);
        break;
      case "ArrowUp":
        next = row(index - 1);
        break;
      case "Home":
        next = row(0);
        break;
      case "End":
        next = row(runs.length - 1);
        break;
      case "ArrowRight":
        next = at.cell === "row" ? { run: at.run, cell: "pill" } : null;
        break;
      case "ArrowLeft":
        next = at.cell === "pill" ? { run: at.run, cell: "row" } : null;
        break;
      default:
        return;
    }
    event.preventDefault();
    if (next === null) {
      return;
    }
    const cell = cellIn(listRef.current, next.run, next.cell);
    if (cell !== null && cell.getAttribute("aria-disabled") !== "true") {
      cell.focus();
    }
  }

  function handleClose() {
    setTarget(null);
  }

  function handleMerge(run: InboxRun) {
    clearRefusal(run);
    setTarget({ run });
  }

  function handleConfirm(run: InboxRun) {
    if (run.head_sha !== undefined) {
      void merge(run, run.head_sha);
    }
  }

  function handleSheetOpen(run: InboxRun) {
    setSheet({ run });
  }

  // However the sheet closed, focus goes back to the row that opened it.
  function handleSheetClose() {
    const run = sheet?.run;
    setSheet(null);
    if (run !== undefined) {
      cellIn(listRef.current, run.run, "row")?.focus();
    }
  }

  return (
    <section aria-labelledby={headingId} className="min-w-0">
      <div className="flex items-center justify-between gap-4">
        <h2
          id={headingId}
          ref={headingRef}
          tabIndex={-1}
          className="font-mono text-label font-semibold text-ink2"
        >
          ACTIONS
        </h2>
        {state.status === "ready" && (
          <span className="inline-flex h-6.5 items-center rounded-full bg-track px-2.5 text-body font-semibold text-ink">
            <span className="sr-only">Waiting: </span>
            {state.runs.length}
          </span>
        )}
      </div>
      <p role="status" className="sr-only">
        {announcement}
      </p>
      {state.status === "loading" && (
        <p role="status" className="mt-4 text-body">
          Reading the inbox…
        </p>
      )}
      {state.status === "error" && (
        <div className="mt-4">
          <p role="alert" className="text-body">
            The inbox could not be read.
          </p>
          <TryAgainPill onClick={retry} />
        </div>
      )}
      {state.status === "ready" && (
        <>
          {state.stale && (
            <p role="status" className="mt-4 text-cap text-defer">
              The list could not be refreshed; showing the last read.
            </p>
          )}
          <div className="card-frame mt-4">
            {state.runs.length === 0 ? (
              <p className="flex min-h-13 items-center text-body">No PR waits on you.</p>
            ) : (
              <ul ref={listRef} onKeyDown={handleKeyDown} onFocus={handleFocus} onBlur={handleBlur}>
                {state.runs.map((run) => (
                  <InboxRow
                    key={run.run}
                    run={run}
                    now={state.readAt}
                    state={rows[run.run]}
                    stop={stop?.run === run.run ? stop.cell : undefined}
                    onOpen={handleSheetOpen}
                    onMerge={handleMerge}
                    onRetry={merge}
                  />
                ))}
              </ul>
            )}
          </div>
        </>
      )}
      <MergeDialog opened={target} onClose={handleClose} onConfirm={handleConfirm} />
      <RowSheet opened={sheet} onClose={handleSheetClose} />
    </section>
  );
}
