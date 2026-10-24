import { postJSON } from "../post-json";
import type { InboxRun } from "./run";

/** What a row shows while its cancel is under way, or why it did not go through. */
export type CancelState = { status: "sending" } | { status: "failed"; words: string };

/** What the surface answered a cancel with. */
export type CancelOutcome =
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
// no longer listed, 409 one that is not waiting, 503 a surface with no run store, 500 a write it
// could not make, and the rest the surface refusing it. retry says whether sending it again can
// change the answer.
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

// sendCancel asks the surface to cancel run, as the owner it is signed in as. A surface that has
// already stopped the run waiting on the owner says so rather than cancelling it twice.
export async function sendCancel(run: InboxRun): Promise<CancelOutcome> {
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

// announcementOf is what the live region tells a cancel: the cancel made, the answer that the run
// had already stopped waiting, or why it did not go through.
export function announcementOf(run: InboxRun, outcome: CancelOutcome): string {
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
