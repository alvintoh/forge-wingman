import { useEffect, useId, useRef, useState } from "react";
import { useInbox } from "../inbox-context";
import { TryAgainPill } from "../pill-button";
import { useProjectFilter } from "../project-filter";
import { CancelDialog } from "./cancel-dialog";
import { type CancelState, announcementOf, sendCancel } from "./cancel-state";
import {
  type Cell,
  LIST_DIALOG_ATTR,
  type Opened,
  type Position,
  cellIn,
  positionOf,
} from "./keyboard";
import { type KindChip, emptyWords, filterRuns, selectionWords } from "./kind";
import { KindChips } from "./kind-chips";
import { MergeDialog } from "./merge-dialog";
import { type Composing, RetryDialog } from "./retry-dialog";
import { InboxRow } from "./row";
import { RowSheet } from "./row-sheet";
import type { InboxRun } from "./run";

type SectionProps = { className: string };

// InboxSection is the inbox's ACTIONS section: the PRs that wait on the owner, and the runs that
// stopped. A row opens its sheet of actions; its MERGE PR opens its confirm and then shows
// the merge on the row, its RETRY, on a stopped run, opens the note for a new attempt, and the
// sheet's Cancel run opens its confirm and then drops the waiting run from the list.
//
// The list is one tab stop, the APG layout grid: the arrow keys move between the rows and from a row
// to its pill and back, Home and End to the first and last row, and the stop stays on the cell
// last focused. The list shows the runs of the project chosen in the URL and of the kind chosen above
// it.
export function InboxSection({ className }: SectionProps) {
  const {
    state,
    refresh,
    retry,
    rows,
    announcement,
    merge,
    clearRefusal,
    retries,
    draftOf,
    setDraft,
    clearRetryFailure,
    sendRetry,
  } = useInbox();
  const [target, setTarget] = useState<Opened | null>(null);
  const [composing, setComposing] = useState<Composing | null>(null);
  const [sheet, setSheet] = useState<Opened | null>(null);
  const [cancelling, setCancelling] = useState<Opened | null>(null);
  // What each row says about a cancel: under way, or why it did not go through.
  const [cancels, setCancels] = useState<Record<string, CancelState>>({});
  const [cancelAnnouncement, setCancelAnnouncement] = useState("");
  const sendingCancel = useRef(new Set<string>());
  const [active, setActive] = useState<Position | null>(null);
  const { project } = useProjectFilter();
  const [chip, setChip] = useState<KindChip | null>(null);
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

  const runs = state.status === "ready" ? filterRuns(state.runs, project, chip) : [];
  const filtering = project !== null || chip !== null;
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

  function handleComposeOpen(run: InboxRun, from: Cell) {
    clearRetryFailure(run);
    setComposing({ run, from });
  }

  function handlePillCompose(run: InboxRun) {
    handleComposeOpen(run, "pill");
  }

  function handleSheetCompose(run: InboxRun) {
    handleComposeOpen(run, "row");
  }

  // However the dialog closed, focus goes back to the cell that opened it.
  function handleComposeClose() {
    const from = composing;
    setComposing(null);
    if (from !== null) {
      cellIn(listRef.current, from.run.run, from.from)?.focus();
    }
  }

  // The dialog stays open while the note is sent, and closes when the run is queued again, unless
  // the owner has since opened another row's.
  async function handleRetryConfirm(run: InboxRun, note: string) {
    if (await sendRetry(run, note)) {
      setComposing((open) => (open?.run.run === run.run ? null : open));
    }
  }

  function handleCancel(run: InboxRun) {
    setCancelling({ run });
  }

  function handleCancelClose() {
    setCancelling(null);
  }

  function handleCancelConfirm(run: InboxRun) {
    void cancel(run);
  }

  // cancel posts one cancel. The row says it is under way and then either leaves with the re-read or
  // says why it did not go through; a failure a second send cannot change re-reads the list.
  async function cancel(run: InboxRun) {
    if (sendingCancel.current.has(run.run)) {
      return;
    }
    sendingCancel.current.add(run.run);
    setCancels((previous) => ({ ...previous, [run.run]: { status: "sending" } }));
    const outcome = await sendCancel(run);
    sendingCancel.current.delete(run.run);
    setCancelAnnouncement(announcementOf(run, outcome));
    if (outcome.status === "failed") {
      setCancels((previous) => ({
        ...previous,
        [run.run]: { status: "failed", words: outcome.words },
      }));
      if (!outcome.retry) {
        refresh();
      }
      return;
    }
    setCancels((previous) => {
      const next = { ...previous };
      delete next[run.run];
      return next;
    });
    refresh();
  }

  const sheetRun = sheet?.run.run;
  const busy =
    sheetRun !== undefined &&
    (rows[sheetRun]?.status === "merging" || cancels[sheetRun]?.status === "sending");

  // However the sheet closed, focus goes back to the row that opened it.
  function handleSheetClose() {
    const run = sheet?.run;
    setSheet(null);
    if (run !== undefined) {
      cellIn(listRef.current, run.run, "row")?.focus();
    }
  }

  const shown =
    state.status === "ready" && filtering
      ? `Showing ${runs.length} of ${state.runs.length} actions`
      : "";

  return (
    <section aria-labelledby={headingId} className={`min-w-0 ${className}`}>
      <div className="flex min-h-6.5 items-start justify-between gap-4">
        <h2
          id={headingId}
          ref={headingRef}
          tabIndex={-1}
          className="min-w-0 py-[3.25px] font-mono text-label font-semibold text-ink2 uppercase wrap-break-word"
        >
          {["actions", ...selectionWords(project, chip)].join(", ")}
        </h2>
        {state.status === "ready" && (
          <span className="inline-flex h-6.5 shrink-0 items-center rounded-full bg-track px-2.5 text-body font-semibold text-ink">
            <span className="sr-only">Waiting: </span>
            {runs.length}
          </span>
        )}
      </div>
      <div>
        <p role="status" className="sr-only">
          {announcement}
        </p>
        <p role="status" className="sr-only">
          {cancelAnnouncement}
        </p>
        <p role="status" className="sr-only">
          {shown}
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
              <div className="flex min-h-13 items-center border-b border-hair">
                <KindChips chip={chip} onChange={setChip} />
              </div>
              {runs.length > 0 && (
                <ul
                  ref={listRef}
                  onKeyDown={handleKeyDown}
                  onFocus={handleFocus}
                  onBlur={handleBlur}
                >
                  {runs.map((run) => (
                    <InboxRow
                      key={run.run}
                      run={run}
                      now={state.readAt}
                      state={rows[run.run]}
                      cancel={cancels[run.run]}
                      stop={stop?.run === run.run ? stop.cell : undefined}
                      onOpen={handleSheetOpen}
                      onMerge={handleMerge}
                      onRetry={merge}
                      onCompose={handlePillCompose}
                    />
                  ))}
                </ul>
              )}
              {runs.length === 0 && !filtering && (
                <p className="flex min-h-13 items-center text-body">No PR waits on you.</p>
              )}
              {runs.length === 0 && filtering && (
                <p className="flex min-h-18 items-center text-body font-semibold">
                  {emptyWords(project, chip)}
                </p>
              )}
            </div>
          </>
        )}
        <MergeDialog opened={target} onClose={handleClose} onConfirm={handleConfirm} />
        <RetryDialog
          opened={composing}
          state={composing === null ? undefined : retries[composing.run.run]}
          draftOf={draftOf}
          onDraft={setDraft}
          onClose={handleComposeClose}
          onConfirm={handleRetryConfirm}
        />
        <CancelDialog
          opened={cancelling}
          onClose={handleCancelClose}
          onConfirm={handleCancelConfirm}
        />
        <RowSheet
          opened={sheet}
          onClose={handleSheetClose}
          onRetry={handleSheetCompose}
          onCancel={handleCancel}
          busy={busy}
        />
      </div>
    </section>
  );
}
