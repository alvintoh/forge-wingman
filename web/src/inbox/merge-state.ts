import { useCallback, useEffect, useRef, useState } from "react";
import { postJSON } from "../post-json";
import type { InboxRun } from "./run";

const MERGED_HOLD_MS = 3_000;

// Why a merge the server refused with 409 cannot go ahead; a retry cannot change any of them.
const refusalWords: Record<string, string> = {
  "no-pull-request": "This run has no pull request to merge.",
  "not-open": "The pull request is no longer open.",
  draft: "The pull request is still a draft.",
  "head-moved": "The pull request has new commits since this list was read.",
  "unconfirmed-decision": "The run's decision is not confirmed yet.",
  "checks-not-green": "The checks are not green.",
};

function isRefusal(body: unknown): body is { reason: string; detail?: string } {
  return (
    typeof body === "object" &&
    body !== null &&
    "reason" in body &&
    typeof body.reason === "string" &&
    (!("detail" in body) || typeof body.detail === "string")
  );
}

// What a row shows once its MERGE PR is confirmed. sha is the head commit the owner confirmed.
export type RowState =
  | { status: "merging"; sha: string }
  | { status: "merged" }
  | { status: "failed"; words: string; sha: string; retry: boolean };

type Failure = { words: string; retry: boolean };

// failureOf words a merge that did not go through. 409 is the server's refusal; 502 is GitHub not
// being asked; the rest is the surface itself. retry says whether sending it again can change the
// answer.
async function failureOf(response: Response): Promise<Failure> {
  if (response.status === 409) {
    const body: unknown = await response.json().catch(() => null);
    if (isRefusal(body) && body.reason === "github-refused") {
      const words = body.detail
        ? `GitHub refused the merge: ${body.detail}`
        : "GitHub refused the merge.";
      return { words, retry: true };
    }
    const words: string | undefined = isRefusal(body) ? refusalWords[body.reason] : undefined;
    if (words !== undefined) {
      return { words, retry: false };
    }
    return {
      words: isRefusal(body) ? `The merge was refused: ${body.reason}.` : "The merge was refused.",
      retry: false,
    };
  }
  if (response.status === 404) {
    return { words: "This run is no longer listed.", retry: false };
  }
  if (response.status === 503) {
    return { words: "Merging is not set up on this surface.", retry: false };
  }
  if (response.status === 502) {
    return { words: "GitHub could not be asked. Try again.", retry: true };
  }
  return { words: "The merge was not made. Try again.", retry: true };
}

export type Merges = {
  // Each row's merge state, keyed by run, so a response only ever writes its own row.
  rows: Record<string, RowState>;
  // The words the live region tells, so a row that leaves with its merge still announces it.
  announcement: string;
  // Tells the live region something; the retries share it.
  announce: (words: string) => void;
  merge: (run: InboxRun, sha: string) => Promise<void>;
  clearRefusal: (run: InboxRun) => void;
};

// useMerges holds the merges made from the list beside the read itself, so a merge in flight and
// the re-read after it outlive the Overview being closed and opened again.
export function useMerges(refresh: () => void): Merges {
  const [rows, setRows] = useState<Record<string, RowState>>({});
  const [announcement, setAnnouncement] = useState("");
  const sending = useRef(new Set<string>());
  const holds = useRef(new Map<string, ReturnType<typeof setTimeout>>());

  useEffect(() => {
    const timers = holds.current;
    return () => {
      for (const timer of timers.values()) {
        clearTimeout(timer);
      }
      timers.clear();
    };
  }, []);

  // Opening a row's confirm clears the words of a refusal a retry could not change.
  const clearRefusal = useCallback((run: InboxRun) => {
    setRows((previous) => {
      const shown = previous[run.run];
      if (shown?.status !== "failed" || shown.retry) {
        return previous;
      }
      const next = { ...previous };
      delete next[run.run];
      return next;
    });
  }, []);

  // merge posts the head commit the owner confirmed; the server merges only if it is still the PR's
  // head. A refusal a retry cannot change means the list is stale, so it is read again.
  const merge = useCallback(
    async (run: InboxRun, sha: string) => {
      if (sending.current.has(run.run)) {
        return;
      }
      sending.current.add(run.run);
      const where = `${run.ticket} in ${run.repo}`;
      const settle = (next: RowState) => setRows((previous) => ({ ...previous, [run.run]: next }));
      settle({ status: "merging", sha });
      setAnnouncement(`Merging ${where}.`);
      let failure: Failure;
      try {
        const response = await postJSON(`/api/runs/${encodeURIComponent(run.run)}/merge`, {
          expected_head_sha: sha,
        });
        if (response.ok) {
          settle({ status: "merged" });
          setAnnouncement(`Merged ${where}.`);
          // The merged row leaves with the re-read; the list moves the focus it held.
          holds.current.set(
            run.run,
            setTimeout(() => {
              holds.current.delete(run.run);
              refresh();
            }, MERGED_HOLD_MS),
          );
          return;
        }
        failure = await failureOf(response);
      } catch (err: unknown) {
        console.error("mergeNotMade", err);
        failure = { words: "The merge could not be sent. Try again.", retry: true };
      } finally {
        sending.current.delete(run.run);
      }
      settle({ status: "failed", words: failure.words, sha, retry: failure.retry });
      setAnnouncement(`${where} was not merged. ${failure.words}`);
      if (!failure.retry) {
        refresh();
      }
    },
    [refresh],
  );

  return { rows, announcement, announce: setAnnouncement, merge, clearRefusal };
}
