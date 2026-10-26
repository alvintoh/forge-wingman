import { useCallback, useRef, useState } from "react";
import { postJSON } from "../post-json";
import type { InboxRun } from "./run";

/** What a row shows while its cancel is under way, once it is made, or why it did not go through. */
export type CancelState =
  | { status: "sending" }
  | { status: "cancelled" }
  | { status: "failed"; words: string };

// What the surface answered a cancel with.
type CancelOutcome =
  | { status: "cancelled" }
  | { status: "already-cancelled" }
  | { status: "already-merged" }
  | { status: "failed"; words: string; retry: boolean };

// A cancel that did not go through: the words the row says, and whether sending it again could
// change the answer.
type Failure = { words: string; retry: boolean };

function isAnswer(body: unknown): body is { status: string } {
  if (typeof body !== "object" || body === null) {
    return false;
  }
  return "status" in body && typeof body.status === "string";
}

// failureOf words a cancel that did not go through: 401 a request that lost its sign-in, 404 a run
// no longer listed, 409 one that neither waits on the owner nor has stopped for it, 503 a surface
// with no run store, 500 a write it could not make, and the rest the surface refusing it. retry
// says whether sending it again can change the answer.
function failureOf(response: Response): Failure {
  if (response.status === 401) {
    return { words: "You are not signed in.", retry: false };
  }
  if (response.status === 404) {
    return { words: "The run is no longer listed.", retry: false };
  }
  if (response.status === 409) {
    return { words: "The run is no longer waiting on you.", retry: false };
  }
  if (response.status === 503) {
    return { words: "Cancelling is not set up on this surface.", retry: false };
  }
  if (response.status === 500) {
    return { words: "The surface could not record it. Try again.", retry: true };
  }
  return { words: "The surface refused it. Try again.", retry: true };
}

// sendCancel asks the surface to cancel run, as the owner it is signed in as. A run already cancelled
// or merged is answered as such rather than cancelled twice.
async function sendCancel(run: InboxRun): Promise<CancelOutcome> {
  let response: Response;
  try {
    response = await postJSON(`/api/runs/${encodeURIComponent(run.run)}/cancel`, {});
  } catch (err: unknown) {
    console.error("cancelNotSent", err);
    return { status: "failed", words: "The request did not reach the surface.", retry: true };
  }
  if (!response.ok) {
    return { status: "failed", ...failureOf(response) };
  }
  const body: unknown = await response.json().catch(() => null);
  const status = isAnswer(body) ? body.status : "";
  // Only what the endpoint documents counts. An answer nothing here knows is a
  // failure, so a row never reports a cancel that may not have happened.
  switch (status) {
    case "cancelled":
      return { status: "cancelled" };
    case "already-cancelled":
      return { status: "already-cancelled" };
    case "already-merged":
      return { status: "already-merged" };
    default:
      return { status: "failed", words: "The answer could not be read.", retry: true };
  }
}

// announcementOf is what the live region tells a cancel: the cancel made, the answer that the run was
// already cancelled or merged, or why it did not go through.
function announcementOf(run: InboxRun, outcome: CancelOutcome): string {
  const where = `${run.ticket} in ${run.repo}`;
  switch (outcome.status) {
    case "cancelled":
      return `Cancelled ${where}.`;
    case "already-cancelled":
      return `${where} was already cancelled.`;
    case "already-merged":
      return `${where} was already merged.`;
    default:
      return `${where} was not cancelled. ${outcome.words}`;
  }
}

/** cancelKey is where a run's cancel state is kept: its own attempt's, never an earlier one's. */
export function cancelKey(run: InboxRun): string {
  return `${run.run}#${run.attempt ?? 0}`;
}

/** isCancelHeld is a cancel that is out or made, so its row takes no action until it leaves. */
export function isCancelHeld(state: CancelState | undefined): boolean {
  return state?.status === "sending" || state?.status === "cancelled";
}

export type Cancels = {
  // Each row's cancel in flight, made or refused, keyed by cancelKey, so a response only ever writes
  // its own row.
  cancels: Record<string, CancelState>;
  cancel: (run: InboxRun) => Promise<void>;
};

// useCancels holds the cancels made from the list beside the read itself, so a cancel in flight, a
// cancel made and the words of one that failed outlive the Overview being closed and opened again.
// announce is the live region merges and retries use as well.
export function useCancels(refresh: () => void, announce: (words: string) => void): Cancels {
  const [cancels, setCancels] = useState<Record<string, CancelState>>({});
  const sending = useRef(new Set<string>());

  // cancel posts one cancel. The row says it is under way and then says it is cancelled until the
  // re-read removes it, or says why it did not go through; a failure a second send cannot change
  // re-reads the list.
  const cancel = useCallback(
    async (run: InboxRun) => {
      const key = cancelKey(run);
      if (sending.current.has(key)) {
        return;
      }
      sending.current.add(key);
      const settle = (next: CancelState | undefined) =>
        setCancels((previous) => {
          const rest = { ...previous };
          if (next === undefined) {
            delete rest[key];
            return rest;
          }
          return { ...rest, [key]: next };
        });
      settle({ status: "sending" });
      const outcome = await sendCancel(run);
      sending.current.delete(key);
      announce(announcementOf(run, outcome));
      if (outcome.status === "failed") {
        settle({ status: "failed", words: outcome.words });
        if (!outcome.retry) {
          refresh();
        }
        return;
      }
      settle(outcome.status === "already-merged" ? undefined : { status: "cancelled" });
      refresh();
    },
    [announce, refresh],
  );

  return { cancels, cancel };
}
