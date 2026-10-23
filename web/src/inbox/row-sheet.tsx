import { useCallback, useEffect, useId, useRef } from "react";
import { createPortal } from "react-dom";
import { Chevron } from "./chevron";
import { type Opened, listDialog } from "./keyboard";
import { type InboxRun, isStopped, parsePull, titleOf } from "./run";

// One action of a row's sheet, a 68px row under a rule: the label at its 28px baseline, the detail
// at 48, the chevron from 30 to 42. An action is a link or a button, with the same class and body.
const sheetActionClass = "flex min-h-[67px] gap-3.5 rounded-tile pt-[11.9px] pb-4";

type SheetActionBodyProps = {
  label: string;
  detail: string;
  // The label's ink: the stop colour for the one action that ends the run.
  tone?: string;
};

function SheetActionBody({ label, detail, tone = "text-ink" }: SheetActionBodyProps) {
  return (
    <>
      <span className="min-w-0 flex-1">
        <span className={`block text-body leading-5 wrap-break-word ${tone}`}>{label}</span>
        <span className="mt-[3.1px] block text-cap leading-4 wrap-break-word text-muted">
          {detail}
        </span>
      </span>
      <Chevron className="mt-[16.6px] mr-px" />
    </>
  );
}

type SheetProps = {
  opened: Opened | null;
  onClose: () => void;
  onRetry: (run: InboxRun) => void;
  onCancel: (run: InboxRun) => void;
  // Whether a merge or a cancel of the opened run is under way, which a cancel would race.
  busy: boolean;
};

// RowSheet is a row's actions, opened by the row: retry with a note when the run can be retried, open
// its pull request when it has one, and cancel it while it waits on the owner. It opens when `opened`
// is set with the focus on its first action, or on the close control when the row has none; an
// action, the close control, Escape and a press on the scrim close it, and the dialog hands the focus
// back to the row.
export function RowSheet({ opened, onClose, onRetry, onCancel, busy }: SheetProps) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const firstRef = useRef<HTMLElement | null>(null);
  const setFirst = useCallback((action: HTMLElement | null) => {
    firstRef.current = action;
  }, []);
  const closeRef = useRef<HTMLButtonElement>(null);
  const titleId = useId();
  const descriptionId = useId();
  const run = opened?.run ?? null;

  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) {
      return;
    }
    if (opened === null) {
      if (dialog.open) {
        dialog.close();
      }
      return;
    }
    if (!dialog.open) {
      dialog.showModal();
    }
    // Each opening, a reopen included, starts on the first action, or on Close when there is none.
    (firstRef.current ?? closeRef.current)?.focus();
  }, [opened]);

  function close() {
    dialogRef.current?.close();
  }

  function retry() {
    if (run !== null) {
      onRetry(run);
    }
    close();
  }

  function cancel() {
    if (run !== null) {
      onCancel(run);
    }
    close();
  }

  // The close event is queued after the dialog closes, so it can land after the row opened the sheet
  // again; only a close that left the dialog shut is one.
  function handleClose() {
    if (dialogRef.current?.open !== true) {
      onClose();
    }
  }

  // A press on the dialog itself lands on its padding or, outside its box, on the scrim.
  function handleClick(event: React.MouseEvent<HTMLDialogElement>) {
    if (event.target !== event.currentTarget) {
      return;
    }
    const box = event.currentTarget.getBoundingClientRect();
    const inside =
      event.clientX >= box.left &&
      event.clientX <= box.right &&
      event.clientY >= box.top &&
      event.clientY <= box.bottom;
    if (!inside) {
      close();
    }
  }

  const pull = run === null ? null : parsePull(run.pr_url);
  const retryable = run?.retryable === true;
  // Only a run waiting on the owner's merge can be cancelled; a stopped one is retried instead.
  const cancellable = run !== null && !isStopped(run) && !busy;

  // From the drawing: the repository's capitals on a 36px baseline, the ticket's on 60, the title's
  // on 82, the first action's rule at 104 and the sheet's end 14 under the last; the close control's
  // 44px circle 16 from the top and on the content's right edge, so the lines beside it stop short.
  return createPortal(
    <dialog
      ref={dialogRef}
      {...listDialog}
      aria-labelledby={titleId}
      aria-describedby={descriptionId}
      onClose={handleClose}
      onClick={handleClick}
      className="m-auto max-h-dvh w-full max-w-[min(28.75rem,calc(100dvw-2rem))] overflow-auto rounded-card border border-hair bg-card px-6.25 pt-[22.1px] pb-3.25 text-ink backdrop:bg-ink/45"
    >
      {run !== null && (
        <>
          <p className="pr-14 font-mono text-label leading-4 font-semibold tracking-[1.4px] wrap-break-word text-muted uppercase">
            {pull === null ? run.repo : pull.repository}
          </p>
          <h2 id={titleId} className="mt-[2.5px] pr-14 text-head leading-6 font-semibold">
            {run.ticket}
          </h2>
          <p
            id={descriptionId}
            className="mt-[3.9px] text-cap leading-4 wrap-break-word text-muted"
          >
            {titleOf(run)}
          </p>
          <ul className="mt-[18.5px]">
            {pull === null && !retryable && (
              <li className="border-t border-hair pt-3 text-label leading-5 text-muted">
                {run.pr_url === ""
                  ? "This run has no pull request to open."
                  : "This pull request's link cannot be opened here."}
              </li>
            )}
            {retryable && (
              <li className="border-t border-hair">
                <button
                  ref={setFirst}
                  type="button"
                  onClick={retry}
                  className={`w-full cursor-pointer text-left ${sheetActionClass}`}
                >
                  <SheetActionBody
                    label="Retry with a note"
                    detail="Start a new attempt with guidance"
                  />
                </button>
              </li>
            )}
            {pull !== null && (
              <li className="border-t border-hair">
                <a
                  ref={retryable ? undefined : setFirst}
                  href={run.pr_url}
                  target="_blank"
                  rel="noopener noreferrer"
                  onClick={close}
                  className={sheetActionClass}
                >
                  <SheetActionBody label="Open pull request" detail="Review the diff on GitHub" />
                  <span className="sr-only"> (opens in a new tab)</span>
                </a>
              </li>
            )}
            {cancellable && (
              <li className="border-t border-hair">
                <button
                  ref={retryable || pull !== null ? undefined : setFirst}
                  type="button"
                  onClick={cancel}
                  className={`w-full cursor-pointer text-left ${sheetActionClass}`}
                >
                  <SheetActionBody
                    label="Cancel run"
                    detail="Stop and record, no partial action"
                    tone="text-stop"
                  />
                </button>
              </li>
            )}
          </ul>
          <button
            ref={closeRef}
            type="button"
            aria-label="Close"
            onClick={close}
            className="absolute top-3.75 right-6.25 flex size-11 cursor-pointer items-center justify-center rounded-full border border-hair bg-ground text-ink2 transition-colors duration-150 hover:bg-track"
          >
            <svg
              aria-hidden="true"
              width="11"
              height="11"
              viewBox="0 0 11 11"
              className="overflow-visible"
            >
              <path
                d="M0 0l11 11M11 0l-11 11"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.8"
                strokeLinecap="round"
              />
            </svg>
          </button>
        </>
      )}
    </dialog>,
    document.body,
  );
}
