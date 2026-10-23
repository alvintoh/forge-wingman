import { useEffect, useId, useRef } from "react";
import { createPortal } from "react-dom";
import { ActionButton, ButtonPair } from "../action-button";
import { type Opened, listDialog } from "./keyboard";
import type { InboxRun } from "./run";

type DialogProps = {
  opened: Opened | null;
  onClose: () => void;
  onConfirm: (run: InboxRun) => void;
};

// CancelDialog confirms one cancel. It opens when `opened` is set; CANCEL RUN hands the run to
// onConfirm and closes it, and Escape and BACK close it without one.
export function CancelDialog({ opened, onClose, onConfirm }: DialogProps) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const titleId = useId();
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
  }, [opened]);

  // The close event is queued after the dialog closes, so it can land after the sheet opened the
  // dialog again; only a close that left the dialog shut is one.
  function handleClose() {
    if (dialogRef.current?.open !== true) {
      onClose();
    }
  }

  function back() {
    dialogRef.current?.close();
  }

  function confirm() {
    if (run !== null) {
      onConfirm(run);
    }
    dialogRef.current?.close();
  }

  // In the body, so the inbox section moving between columns never takes an open modal with it.
  return createPortal(
    <dialog
      ref={dialogRef}
      {...listDialog}
      aria-labelledby={titleId}
      onClose={handleClose}
      className="m-auto max-h-dvh w-full max-w-[min(28.75rem,calc(100dvw-2rem))] overflow-auto rounded-card border border-hair bg-card px-6.25 pt-7.25 pb-5.25 text-ink backdrop:bg-ink/45"
    >
      {run !== null && (
        <>
          <h2 id={titleId} className="text-head leading-6 font-semibold">
            Cancel this run?
          </h2>
          <p className="mt-1.75 text-label leading-5 font-semibold wrap-break-word">{run.ticket}</p>
          <p className="mt-2 text-label leading-4.5 text-ink2">
            The pull request is left as it is.
          </p>
          <ButtonPair
            margin="mt-2"
            back={<ActionButton kind="secondary" label="BACK" onClick={back} />}
            action={<ActionButton kind="destructive" label="CANCEL RUN" onClick={confirm} />}
          />
        </>
      )}
    </dialog>,
    document.body,
  );
}
