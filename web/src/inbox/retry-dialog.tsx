import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { ActionButton, ButtonPair } from "../action-button";
import { type Cell, type Opened, listDialog } from "./keyboard";
import type { RetryState } from "./retry-state";
import { type InboxRun, isStopped, stopReasonOf, titleOf } from "./run";

// The note's limits: the field stops at 2000 characters, and the server takes 4096 bytes of the
// trimmed note, which a multibyte note can pass first.
const MAX_NOTE_CHARS = 2000;
const MAX_NOTE_BYTES = 4096;
const SENDING_LABEL = "SENDING";
const TOO_LONG_WORDS = "The note is too long to send.";

const labelClass = "block font-mono text-label leading-4 font-semibold tracking-[1.4px] text-ink2";

const encoder = new TextEncoder();

// Which cell of the list opened the dialog, so the focus goes back to it.
export type Composing = Opened & { from: Cell };

type DialogProps = {
  opened: Composing | null;
  state: RetryState | undefined;
  draftOf: (run: string) => string;
  onDraft: (run: string, note: string) => void;
  onClose: () => void;
  onConfirm: (run: InboxRun, note: string) => void;
};

type FormProps = Omit<DialogProps, "opened" | "onClose"> & { run: InboxRun; onBack: () => void };

// reasonSentence is why the run is here and what the new attempt does.
function reasonSentence(run: InboxRun): string {
  const clause = isStopped(run) ? stopReasonOf(run) : "Its pull request is still a draft";
  const closes = run.pr_url === "" ? "" : "; its draft PR is closed once the new PR opens";
  return `${clause}. The new attempt starts from the ticket and your note${closes}.`;
}

// A restored note is carried on, so the caret starts after it.
function caretAtEnd(field: HTMLTextAreaElement | null) {
  field?.setSelectionRange(field.value.length, field.value.length);
}

function RetryForm({ run, state, draftOf, onDraft, onConfirm, onBack }: FormProps) {
  const [note, setNote] = useState(() => draftOf(run.run));
  const noteId = useId();
  const helpId = useId();
  const alertId = useId();

  const trimmed = note.trim();
  const tooLong = encoder.encode(trimmed).length > MAX_NOTE_BYTES;
  const sending = state?.status === "sending";
  const blocked = trimmed === "" || tooLong || sending;
  const words = tooLong ? TOO_LONG_WORDS : state?.status === "failed" ? state.words : undefined;

  function handleChange(event: React.ChangeEvent<HTMLTextAreaElement>) {
    setNote(event.target.value);
    onDraft(run.run, event.target.value);
  }

  function confirm() {
    if (!blocked) {
      onConfirm(run, trimmed);
    }
  }

  return (
    <>
      <p className="mt-1.75 text-label leading-5 font-semibold wrap-break-word">
        {run.repo} · {run.ticket}
      </p>
      <p className="mt-0.5 text-cap leading-4 wrap-break-word text-muted">{titleOf(run)}</p>
      <p className="mt-3 border-t border-hair pt-3 text-label leading-4.5 wrap-break-word text-ink2">
        {reasonSentence(run)}
      </p>
      <label htmlFor={noteId} className={`mt-4.5 ${labelClass}`}>
        YOUR NOTE
      </label>
      <textarea
        ref={caretAtEnd}
        id={noteId}
        value={note}
        maxLength={MAX_NOTE_CHARS}
        placeholder="What should the agent do differently?"
        aria-describedby={helpId}
        onChange={handleChange}
        className="mt-1.5 block max-h-[max(7rem,calc(100dvh-29rem))] min-h-28 w-full resize-none rounded-tile border border-ink2 bg-ground px-3.5 py-3 text-head leading-6 field-sizing-content text-ink placeholder:text-muted"
      />
      <div className="mt-2 flex items-start justify-between gap-3 text-cap leading-4 text-muted">
        <p id={helpId}>For this retry only. The rules are unchanged.</p>
        <p className="shrink-0 font-mono">
          {note.length} / {MAX_NOTE_CHARS}
        </p>
      </div>
      {words !== undefined && (
        <p id={alertId} role="alert" className="mt-3 text-label leading-4.5 text-stop">
          {words}
        </p>
      )}
      <ButtonPair
        back={<ActionButton kind="secondary" label="BACK" onClick={onBack} />}
        action={
          <ActionButton
            kind="primary"
            label={sending ? SENDING_LABEL : "RETRY"}
            aria-disabled={blocked || undefined}
            aria-describedby={words === undefined ? undefined : alertId}
            onClick={confirm}
          />
        }
      />
    </>
  );
}

// RetryDialog composes one retry. It opens when `opened` is set and stays open while the note is
// sent: RETRY waits for the note, a refusal shows in it and keeps the note, and the retry that is
// queued closes it from outside. Escape and BACK close it without sending, and the note is kept.
export function RetryDialog({ opened, onClose, ...form }: DialogProps) {
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

  // The close event is queued after the dialog closes, so it can land after the row opened the
  // dialog again; only a close that left the dialog shut is one.
  function handleClose() {
    if (dialogRef.current?.open !== true) {
      onClose();
    }
  }

  function back() {
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
            Retry with a note?
          </h2>
          <RetryForm key={run.run} run={run} onBack={back} {...form} />
        </>
      )}
    </dialog>,
    document.body,
  );
}
