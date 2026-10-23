import { useCallback, useRef, useState } from "react";
import { postJSON } from "../post-json";
import type { InboxRun } from "./run";

// Why a retry the server refused with 409 (or 503) cannot go ahead; sending it again cannot change them.
const refusalWords: Record<string, string> = {
  "attempt-moved": "This run changed since you opened it. Close and reopen it.",
  "in-flight": "This run is running again.",
  waiting: "This run is waiting on a decision.",
  "already-queued": "This run is already queued.",
  abandoned: "This run was abandoned and needs an operator's requeue.",
  merged: "Its pull request is already merged.",
  "no-changes": "This run made no changes, so a note would not change it.",
  "pr-ready": "Its pull request is ready: merge it instead.",
  "pr-merged": "Its pull request is already merged.",
  "pr-closed": "Its pull request is closed.",
  "no-pull-request": "This run's pull request could not be matched to it.",
  "no-merge-token": "Retrying a run with a pull request is not set up on this surface.",
};

const REFUSED_WORDS = "The retry was refused.";
const NOT_SENT_WORDS = "The retry could not be sent. Your note is kept.";

function reasonOf(body: unknown): string | null {
  return typeof body === "object" &&
    body !== null &&
    "reason" in body &&
    typeof body.reason === "string"
    ? body.reason
    : null;
}

// failureOf words a retry that was not queued.
async function failureOf(response: Response): Promise<string> {
  if (response.status !== 409 && response.status !== 503) {
    return NOT_SENT_WORDS;
  }
  const reason = reasonOf(await response.json().catch(() => null));
  if (reason !== null) {
    return refusalWords[reason] ?? REFUSED_WORDS;
  }
  return response.status === 409 ? REFUSED_WORDS : NOT_SENT_WORDS;
}

// What a row's open retry dialog shows besides its note.
export type RetryState = { status: "sending" } | { status: "failed"; words: string };

export type Retries = {
  // Each row's retry in flight or refused, keyed by run, so a response only ever writes its own row.
  retries: Record<string, RetryState>;
  // The note typed for a run and not sent. It lives in the provider, so closing the dialog or the
  // Overview keeps it and a reload drops it; nothing is stored.
  draftOf: (run: string) => string;
  setDraft: (run: string, note: string) => void;
  // Opening a row's dialog clears the words of an earlier refusal.
  clearRetryFailure: (run: InboxRun) => void;
  // sendRetry resolves true when the server queued the run again.
  sendRetry: (run: InboxRun, note: string) => Promise<boolean>;
};

// useRetries holds the retries made from the list beside the read itself, and the notes typed for
// them. announce is the live region merges use as well.
export function useRetries(refresh: () => void, announce: (words: string) => void): Retries {
  const [retries, setRetries] = useState<Record<string, RetryState>>({});
  const drafts = useRef(new Map<string, string>());
  const sending = useRef(new Set<string>());

  const draftOf = useCallback((run: string) => drafts.current.get(run) ?? "", []);
  const setDraft = useCallback((run: string, note: string) => {
    drafts.current.set(run, note);
  }, []);

  const clearRetryFailure = useCallback((run: InboxRun) => {
    setRetries((previous) => {
      if (previous[run.run]?.status !== "failed") {
        return previous;
      }
      const next = { ...previous };
      delete next[run.run];
      return next;
    });
  }, []);

  const settle = useCallback((run: string, next: RetryState | undefined) => {
    setRetries((previous) => {
      const rest = { ...previous };
      if (next === undefined) {
        delete rest[run];
        return rest;
      }
      return { ...rest, [run]: next };
    });
  }, []);

  // sendRetry posts the note and the attempt the owner saw; the server queues the run only if it is
  // still on it. A refusal means the list is stale, so it is read again.
  const sendRetry = useCallback(
    async (run: InboxRun, note: string) => {
      if (sending.current.has(run.run)) {
        return false;
      }
      sending.current.add(run.run);
      settle(run.run, { status: "sending" });
      let words = NOT_SENT_WORDS;
      let stale = false;
      try {
        const response = await postJSON(`/api/runs/${encodeURIComponent(run.run)}/retry`, {
          note,
          expected_attempt: run.attempt ?? 0,
        });
        if (response.ok) {
          drafts.current.delete(run.run);
          settle(run.run, undefined);
          announce(`Retry queued for ${run.ticket} in ${run.repo}.`);
          refresh();
          return true;
        }
        words = await failureOf(response);
        stale = response.status === 409;
      } catch (err: unknown) {
        console.error("retryNotMade", err);
      } finally {
        sending.current.delete(run.run);
      }
      settle(run.run, { status: "failed", words });
      if (stale) {
        refresh();
      }
      return false;
    },
    [announce, refresh, settle],
  );

  return { retries, draftOf, setDraft, clearRetryFailure, sendRetry };
}
