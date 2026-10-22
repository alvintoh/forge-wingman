import { useCallback, useEffect, useId, useRef, useState } from "react";
import { ActionButton, ButtonPair } from "./action-button";
import { ago } from "./ago";
import { pillSetWidth } from "./button-style";
import { SmallPill, TryAgainPill } from "./pill-button";
import { postJSON } from "./post-json";

type Checks = {
  state: string;
  summary: string;
};

type InboxRun = {
  run: string;
  ticket: string;
  repo: string;
  pr_url: string;
  wait_reason: string;
  updated_at: string;
  title?: string;
  head_sha?: string;
  draft?: boolean;
  checks?: Checks;
};

type InboxState = {
  status: "loading" | "error" | "ready";
  runs: InboxRun[];
  // When the list was read, so every row's age is against one clock.
  readAt: number;
  // A re-read failed while an earlier read is on screen.
  stale: boolean;
};

type Kind = "FORK" | "TODO" | "REVIEW";

type Wait = { words: string; blocks: boolean; kind: Kind };

const DRAFT_WORDS = "still a draft";

// Why a PR waits on the owner. The first three also block the merge.
const waits: Record<string, Wait> = {
  "unconfirmed-decision": { words: "unconfirmed decision", blocks: true, kind: "FORK" },
  draft: { words: DRAFT_WORDS, blocks: true, kind: "TODO" },
  "checks-not-green": { words: "checks not green", blocks: true, kind: "REVIEW" },
  "front-end": { words: "front-end change", blocks: false, kind: "REVIEW" },
  "size-l": { words: "size L", blocks: false, kind: "REVIEW" },
  sampled: { words: "sampled", blocks: false, kind: "REVIEW" },
  "auto-merge-off": { words: "auto-merge is off", blocks: false, kind: "REVIEW" },
};

const kindFill: Record<Kind, string> = {
  FORK: "bg-stop",
  TODO: "bg-defer",
  REVIEW: "bg-review",
};

const UNREAD_WORDS = "GitHub could not be read";
const MERGING_WORDS = "squash in progress";
const MERGED_WORDS = "Merged. Squashed, branch deleted.";

const MERGED_HOLD_MS = 3_000;

// The merge pill takes RETRY MERGE's width, the set the drawing sizes it from.
const MERGE_PILL_WIDTH = pillSetWidth(["MERGE PR", "MERGING…", "RETRY MERGE"]);

// Why a merge the server refused with 409 cannot go ahead; a retry cannot change any of them.
const refusalWords: Record<string, string> = {
  "no-pull-request": "This run has no pull request to merge.",
  "not-open": "The pull request is no longer open.",
  draft: "The pull request is still a draft.",
  "head-moved": "The pull request has new commits since this list was read.",
  "unconfirmed-decision": "The run's decision is not confirmed yet.",
  "checks-not-green": "The checks are not green.",
};

function isChecks(value: unknown): value is Checks {
  return (
    typeof value === "object" &&
    value !== null &&
    "state" in value &&
    typeof value.state === "string" &&
    "summary" in value &&
    typeof value.summary === "string"
  );
}

function isInboxRun(row: unknown): row is InboxRun {
  if (typeof row !== "object" || row === null) {
    return false;
  }
  const fields = new Map(Object.entries(row));
  const text = (key: string) => typeof fields.get(key) === "string";
  const optional = (key: string, kind: string) =>
    !fields.has(key) || typeof fields.get(key) === kind;
  return (
    ["run", "ticket", "repo", "pr_url", "wait_reason", "updated_at"].every(text) &&
    optional("title", "string") &&
    optional("head_sha", "string") &&
    optional("draft", "boolean") &&
    (!fields.has("checks") || isChecks(fields.get("checks")))
  );
}

function isInbox(body: unknown): body is { runs: InboxRun[] } {
  return (
    typeof body === "object" &&
    body !== null &&
    "runs" in body &&
    Array.isArray(body.runs) &&
    body.runs.every(isInboxRun)
  );
}

function isRefusal(body: unknown): body is { reason: string; detail?: string } {
  return (
    typeof body === "object" &&
    body !== null &&
    "reason" in body &&
    typeof body.reason === "string" &&
    (!("detail" in body) || typeof body.detail === "string")
  );
}

function parsePull(prUrl: string): { repository: string; number: string } | null {
  const match = /^https:\/\/github\.com\/([^/]+\/[^/]+)\/pull\/(\d+)\/?(?:[?#].*)?$/.exec(prUrl);
  return match?.[1] !== undefined && match[2] !== undefined
    ? { repository: match[1], number: match[2] }
    : null;
}

// useInbox reads the list. refresh re-reads in place; retry shows the loading panel again first,
// for a read that failed. A re-read skips the browser's copy: the list is cached for 30 seconds,
// and a PR just merged must leave it.
function useInbox() {
  const [state, setState] = useState<InboxState>({
    status: "loading",
    runs: [],
    readAt: 0,
    stale: false,
  });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const controller = new AbortController();

    async function read() {
      const response = await fetch("/api/inbox", {
        signal: controller.signal,
        cache: attempt === 0 ? "default" : "no-cache",
      });
      if (!response.ok) {
        throw new Error(`the inbox endpoint answered ${response.status}`);
      }
      const body: unknown = await response.json();
      if (!isInbox(body)) {
        throw new Error("the inbox endpoint answered no run list");
      }
      if (controller.signal.aborted) {
        return;
      }
      setState({ status: "ready", runs: body.runs, readAt: Date.now(), stale: false });
    }

    read().catch((err: unknown) => {
      if (controller.signal.aborted) {
        return;
      }
      console.error("inboxNotRead", err);
      setState((previous) =>
        previous.status === "ready"
          ? { ...previous, stale: true }
          : { status: "error", runs: [], readAt: 0, stale: false },
      );
    });

    return () => controller.abort();
  }, [attempt]);

  const refresh = useCallback(() => setAttempt((n) => n + 1), []);
  const retry = useCallback(() => {
    setState({ status: "loading", runs: [], readAt: 0, stale: false });
    setAttempt((n) => n + 1);
  }, []);

  return { state, refresh, retry };
}

// What a row shows once its MERGE PR is confirmed. sha is the head commit the owner confirmed.
type RowState =
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

const chipClass =
  "inline-flex h-5.5 shrink-0 items-center rounded-full px-2.5 font-mono text-micro font-semibold tracking-[1.2px] text-card";

type RowProps = {
  run: InboxRun;
  now: number;
  state: RowState | undefined;
  onMerge: (run: InboxRun) => void;
  onRetry: (run: InboxRun, sha: string) => void;
};

function InboxRow({ run, now, state, onMerge, onRetry }: RowProps) {
  const reasonId = useId();
  const wait: Wait | undefined = waits[run.wait_reason];
  const draft = run.draft === true;
  const blocked = wait?.blocks === true || draft;
  const unread = run.head_sha === undefined;
  const pull = parsePull(run.pr_url);
  const title = run.title ?? (pull === null ? "Pull request" : `Pull request #${pull.number}`);
  // A draft on GitHub shows the same chip as a stored draft reason, whatever reason is stored.
  const kind = draft ? "TODO" : (wait?.kind ?? "REVIEW");

  let reason = draft ? DRAFT_WORDS : wait?.words;
  if (!blocked && unread) {
    reason = UNREAD_WORDS;
  }
  let tone = "text-ink2";
  let pillLabel = "MERGE PR";
  let pillName = `Merge PR ${run.ticket} in ${run.repo}`;
  let onPill: (() => void) | undefined = () => onMerge(run);
  if (state?.status === "merging") {
    reason = MERGING_WORDS;
    pillLabel = "MERGING...";
    onPill = undefined;
  } else if (state?.status === "merged") {
    reason = MERGED_WORDS;
    tone = "text-ok";
  } else if (state?.status === "failed") {
    const { sha } = state;
    reason = `Not merged. ${state.words}`;
    tone = "text-stop";
    if (state.retry) {
      pillLabel = "RETRY MERGE";
      pillName = `Retry merge ${run.ticket} in ${run.repo}`;
      onPill = () => onRetry(run, sha);
    }
  }
  const merged = state?.status === "merged";
  // A row with no merge under way, or whose refusal a retry cannot change, has its normal control,
  // computed from the row as last read.
  const normal = state === undefined || (state.status === "failed" && !state.retry);
  const blockedControl = normal && (blocked || unread);
  // A merging or blocked pill is aria-disabled, not disabled, so it keeps the focus through a
  // re-read and stays in the tab order, where its reason is read as its description.
  const ariaDisabled = state?.status === "merging" || blockedControl;
  if (ariaDisabled) {
    onPill = undefined;
  }
  if (state === undefined && blocked) {
    tone = "text-ink";
  }

  return (
    // One PR is one row of the inbox card, laid out from the drawing and measured from the row's
    // top (the card's edge, or the rule above it): the pill at 22.8 in its 44px hit area, the
    // meta baseline at 40.8, the title's at 71.8 and the reason's 22 under it, 24 above the
    // row's end. Each margin below is that gap less the line box around the baseline: 22px for
    // the title (baseline 17.4 down), 16px for the 11px meta line (12.1) and the 13px reason
    // (12.9). pt: 22.8, less the 8px between the hit area and the pill, less the 1px rule or edge.
    // Where the row is under 376px wide the repository and age wrap to a second line, its
    // baseline 22 under the first and the title 9.7 under its box; the hit area's bottom 8px
    // overlaps that line, as the drawing's does.
    // A merged row keeps an empty slot the pill's size, so it is as tall as before the merge.
    <li className="@container border-t border-hair pt-[13.8px] pb-[20.9px] first:border-t-0">
      <div className="grid min-h-11 grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 @min-[23.5rem]:grid-cols-[auto_minmax(0,1fr)_auto] @min-[23.5rem]:gap-x-2">
        <div className="col-start-1 row-start-1 flex items-center gap-3">
          <span className={`${chipClass} ${kindFill[kind]}`}>{kind}</span>
          <span className="font-mono text-micro font-semibold tracking-[0.6px] text-ink2">
            {run.ticket}
          </span>
        </div>
        <p className="col-span-2 row-start-2 -mt-[8.1px] flex min-w-0 items-center gap-2 font-mono text-micro leading-4 text-muted @min-[23.5rem]:col-span-1 @min-[23.5rem]:col-start-2 @min-[23.5rem]:row-start-1 @min-[23.5rem]:mt-0">
          <span aria-hidden="true" className="hidden @min-[23.5rem]:inline">
            /
          </span>
          <span title={run.repo} className="min-w-0 truncate tracking-[0.4px]">
            {run.repo}
          </span>
          <span aria-hidden="true">/</span>
          <span className="shrink-0">{ago(run.updated_at, now, "")}</span>
        </p>
        {merged ? (
          <span
            aria-hidden="true"
            style={MERGE_PILL_WIDTH}
            className="h-11 min-w-11 font-mono text-micro"
          />
        ) : (
          <SmallPill
            kind="primary"
            width={MERGE_PILL_WIDTH}
            label={pillLabel}
            aria-disabled={ariaDisabled || undefined}
            aria-label={pillName}
            aria-describedby={
              reason !== undefined && (blockedControl || state !== undefined) ? reasonId : undefined
            }
            onClick={onPill}
          />
        )}
      </div>
      <p
        title={run.title}
        className="mt-[9.7px] line-clamp-2 text-head leading-5.5 font-semibold wrap-break-word text-ink @min-[23.5rem]:-mt-[4.4px] @min-[23.5rem]:line-clamp-none"
      >
        {title}
      </p>
      {reason !== undefined && (
        <p id={reasonId} className={`mt-[4.5px] text-label leading-4 wrap-break-word ${tone}`}>
          {reason}
        </p>
      )}
    </li>
  );
}

type DialogProps = {
  run: InboxRun | null;
  onClose: () => void;
  onConfirm: (run: InboxRun) => void;
};

// MergeDialog confirms one merge. It opens when `run` is set; MERGE PR hands the run to onConfirm
// and closes it, and Escape and BACK close it without one.
function MergeDialog({ run, onClose, onConfirm }: DialogProps) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const titleId = useId();

  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) {
      return;
    }
    if (run === null) {
      if (dialog.open) {
        dialog.close();
      }
      return;
    }
    if (!dialog.open) {
      dialog.showModal();
    }
  }, [run]);

  function back() {
    dialogRef.current?.close();
  }

  function confirm() {
    if (run !== null) {
      onConfirm(run);
    }
    dialogRef.current?.close();
  }

  const pull = run === null ? null : parsePull(run.pr_url);
  const checks = run?.checks;

  return (
    <dialog
      ref={dialogRef}
      aria-labelledby={titleId}
      onClose={onClose}
      className="m-auto max-h-dvh w-full max-w-[min(28.75rem,calc(100dvw-2rem))] overflow-auto rounded-card border border-hair bg-card px-6.25 pt-7.25 pb-5.25 text-ink backdrop:bg-ink/40"
    >
      {run !== null && (
        <>
          <h2 id={titleId} className="text-head leading-6 font-semibold">
            Merge this PR?
          </h2>
          <p className="mt-1.75 text-label leading-5 font-semibold wrap-break-word">
            {pull === null ? run.repo : `${pull.repository} #${pull.number}`}
          </p>
          {run.title !== undefined && (
            <p className="mt-0.5 text-cap leading-4 wrap-break-word text-muted">{run.title}</p>
          )}
          <ul className="mt-2 text-label leading-4">
            <li className="flex h-7.5 gap-3.5 border-t border-hair pt-[4.6px]">
              <span
                aria-hidden="true"
                className="mt-1.5 ml-0.5 size-1.5 shrink-0 rounded-full bg-ink2"
              />
              Squash and delete the branch
            </li>
            <li className="flex h-7.5 gap-3.5 border-t border-hair pt-[4.6px]">
              <span
                aria-hidden="true"
                className="mt-1.5 ml-0.5 size-1.5 shrink-0 rounded-full bg-ink2"
              />
              Done as you, not the runner
            </li>
            <li className="flex min-h-7.5 gap-3.5 border-t border-hair pt-[4.6px]">
              {checks?.state === "green" ? (
                <svg
                  aria-hidden="true"
                  width="13"
                  height="10"
                  viewBox="0 0 13 10"
                  className="mt-1.5 -mr-1.25 shrink-0 overflow-visible text-ok"
                >
                  <path
                    d="M0 6l4 4l9-10"
                    fill="none"
                    stroke="currentColor"
                    strokeWidth="2"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                  />
                </svg>
              ) : (
                <span
                  aria-hidden="true"
                  className="mt-1.5 ml-0.5 size-1.5 shrink-0 rounded-full bg-ink2"
                />
              )}
              <span className="min-w-0 wrap-break-word">
                {checks === undefined
                  ? "Checks not read"
                  : `Checks ${checks.state}${checks.summary === "" ? "" : `: ${checks.summary}`}`}
              </span>
            </li>
          </ul>
          <p className="mt-3 text-cap leading-4 text-muted">Merging is hard to reverse.</p>
          <ButtonPair
            margin="mt-2"
            back={<ActionButton kind="secondary" label="BACK" onClick={back} />}
            action={<ActionButton kind="primary" label="MERGE PR" onClick={confirm} />}
          />
        </>
      )}
    </dialog>
  );
}

// InboxSection is the inbox's NEEDS YOU section: the PRs that wait on the owner, each with a
// MERGE PR that opens its confirm and then shows the merge on the row.
export function InboxSection() {
  const { state, refresh, retry } = useInbox();
  const [target, setTarget] = useState<InboxRun | null>(null);
  // Each row's merge state, keyed by run, so a response only ever writes its own row.
  const [rows, setRows] = useState<Record<string, RowState>>({});
  // One live region tells a result, so a row that leaves with its merge still announces it.
  const [announcement, setAnnouncement] = useState("");
  const sending = useRef(new Set<string>());
  const holds = useRef(new Map<string, ReturnType<typeof setTimeout>>());
  const headingId = useId();
  const headingRef = useRef<HTMLHeadingElement>(null);

  function handleClose() {
    setTarget(null);
  }

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
  function handleOpen(run: InboxRun) {
    setRows((previous) => {
      const shown = previous[run.run];
      if (shown?.status !== "failed" || shown.retry) {
        return previous;
      }
      const next = { ...previous };
      delete next[run.run];
      return next;
    });
    setTarget(run);
  }

  function handleConfirm(run: InboxRun) {
    if (run.head_sha !== undefined) {
      void merge(run, run.head_sha);
    }
  }

  // merge posts the head commit the owner confirmed; the server merges only if it is still the PR's
  // head. A refusal a retry cannot change means the list is stale, so it is read again.
  async function merge(run: InboxRun, sha: string) {
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
        // The merged row has no control and leaves with the re-read, so focus moves to the heading
        // as the list is read again.
        holds.current.set(
          run.run,
          setTimeout(() => {
            holds.current.delete(run.run);
            refresh();
            headingRef.current?.focus();
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
          NEEDS YOU
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
              <ul>
                {state.runs.map((run) => (
                  <InboxRow
                    key={run.run}
                    run={run}
                    now={state.readAt}
                    state={rows[run.run]}
                    onMerge={handleOpen}
                    onRetry={merge}
                  />
                ))}
              </ul>
            )}
          </div>
        </>
      )}
      <MergeDialog run={target} onClose={handleClose} onConfirm={handleConfirm} />
    </section>
  );
}
