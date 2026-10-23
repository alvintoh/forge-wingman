import { useCallback, useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { ActionButton, ButtonPair } from "./action-button";
import { ago } from "./ago";
import { InboxContext, useInbox } from "./inbox-context";
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
const MERGING_LABEL = "MERGING...";
const MERGE_PILL_WIDTH = pillSetWidth(["MERGE PR", MERGING_LABEL, "RETRY MERGE"]);

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

type InboxRead = {
  state: InboxState;
  refresh: () => void;
  retry: () => void;
};

/** The inbox list, the ways to read it again, and the merges made from it. */
export type Inbox = InboxRead & Merges;

// useInboxRead reads the list. refresh re-reads in place; retry shows the loading panel again
// first, for a read that failed. A re-read skips the browser's copy: the list is cached for 30
// seconds, and a PR just merged must leave it.
function useInboxRead(): InboxRead {
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
        cache: attempt === 0 ? "default" : "reload",
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

function titleOf(run: InboxRun): string {
  const pull = parsePull(run.pr_url);
  return run.title ?? (pull === null ? "Pull request" : `Pull request #${pull.number}`);
}

const chipClass =
  "inline-flex h-5.5 shrink-0 items-center rounded-full px-2.5 font-mono text-micro font-semibold tracking-[1.2px] text-card";

type ChevronProps = {
  className: string;
};

// Chevron is the drawing's row affordance: a 6x12 right chevron, muted, its tip at the box's 7px.
function Chevron({ className }: ChevronProps) {
  return (
    <svg
      aria-hidden="true"
      width="8"
      height="14"
      viewBox="0 0 8 14"
      className={`shrink-0 text-muted ${className}`}
    >
      <path
        d="M1 1l6 6l-6 6"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

// The two focusable cells of a row: the row itself, which opens its sheet, and its MERGE PR pill.
type Cell = "row" | "pill";

// The attributes a cell carries, which the list's keys read back to find where the focus is.
const RUN_ATTR = "data-run";
const CELL_ATTR = "data-cell";

function cellAttrs(run: string, cell: Cell): Record<string, string> {
  return { [RUN_ATTR]: run, [CELL_ATTR]: cell };
}

type RowProps = {
  run: InboxRun;
  now: number;
  state: RowState | undefined;
  // Which of the row's cells is the list's one tab stop, if the stop is on this row.
  stop: Cell | undefined;
  onOpen: (run: InboxRun) => void;
  onMerge: (run: InboxRun) => void;
  onRetry: (run: InboxRun, sha: string) => void;
};

function InboxRow({ run, now, state, stop, onOpen, onMerge, onRetry }: RowProps) {
  const reasonId = useId();
  const ticketId = useId();
  const titleId = useId();
  const wait: Wait | undefined = waits[run.wait_reason];
  const draft = run.draft === true;
  const blocked = wait?.blocks === true || draft;
  const unread = run.head_sha === undefined;
  const title = titleOf(run);
  // A draft on GitHub shows the same chip as a stored draft reason, whatever reason is stored.
  const kind = draft ? "TODO" : (wait?.kind ?? "REVIEW");

  const plainReason = run.wait_reason.replaceAll("-", " ");
  let reason = draft
    ? DRAFT_WORDS
    : (wait?.words ?? (plainReason === "" ? undefined : plainReason));
  if (!blocked && unread) {
    reason = UNREAD_WORDS;
  }
  let tone = "text-ink2";
  let pillLabel = "MERGE PR";
  let pillName = `Merge PR ${run.ticket} in ${run.repo}`;
  let onPill: (() => void) | undefined = () => onMerge(run);
  if (state?.status === "merging") {
    reason = MERGING_WORDS;
    pillLabel = MERGING_LABEL;
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
  // re-read and its reason is read as its description. The arrow keys pass over it.
  const ariaDisabled = state?.status === "merging" || blockedControl;
  if (ariaDisabled) {
    onPill = undefined;
  }
  if (state === undefined && blocked) {
    tone = "text-ink";
  }
  // A merged row has no pill, so a stop left on it falls back to the row.
  const rowStop = stop === "row" || (stop === "pill" && merged);

  function handleOpen() {
    onOpen(run);
  }

  return (
    // One PR is one row of the inbox card, laid out from the drawing and measured from the row's
    // top (the card's edge, or the rule above it): the pill at 22.8 in its 44px hit area, the
    // meta baseline at 40.8, the title's at 71.8 and the reason's 22 under it, 24 above the
    // row's end. Each margin below is that gap less the line box around the baseline: 22px for
    // the title (baseline 17.4 down), 16px for the 11px meta line (12.1) and the 13px reason
    // (12.9). pt: 22.8, less the 8px between the hit area and the pill, less the 1px rule or edge.
    // Where the row is under 376px wide the repository and age wrap to a second line, below the
    // pill's 44px hit area rather than under its bottom 8px, so a tap on the age opens the row's
    // sheet and never the merge; the title sits 9.7 under that line's box.
    // A merged row keeps an empty slot the pill's size, so it is as tall as before the merge.
    <li className="@container relative border-t border-hair pt-[13.8px] pb-[20.9px] first:border-t-0">
      {/* The row is the target that opens its sheet: a button laid over the whole row, out to the
          card's edges, under the pill, which is positioned after it and so painted above it. Its
          ring is the drawing's 3px, centred 6px inside the row, as the card's tile radius. */}
      <button
        type="button"
        tabIndex={rowStop ? 0 : -1}
        {...cellAttrs(run.run, "row")}
        aria-haspopup="dialog"
        aria-labelledby={`${ticketId} ${titleId}`}
        aria-describedby={reason === undefined ? undefined : reasonId}
        onClick={handleOpen}
        className="absolute inset-y-0 -inset-x-padx cursor-pointer rounded-tile focus-visible:outline-offset-[-7.5px]!"
      />
      <div className="grid min-h-11 grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 @min-[23.5rem]:grid-cols-[auto_minmax(0,1fr)_auto] @min-[23.5rem]:gap-x-2">
        <div className="col-start-1 row-start-1 flex items-center gap-3">
          <span className={`${chipClass} ${kindFill[kind]}`}>{kind}</span>
          <span
            id={ticketId}
            className="font-mono text-micro font-semibold tracking-[0.6px] text-ink2"
          >
            {run.ticket}
          </span>
        </div>
        <p className="col-span-2 row-start-2 flex min-w-0 items-center gap-2 font-mono text-micro leading-4 text-muted @min-[23.5rem]:col-span-1 @min-[23.5rem]:col-start-2 @min-[23.5rem]:row-start-1">
          <span aria-hidden="true" className="hidden @min-[23.5rem]:inline">
            /
          </span>
          <span className="min-w-0 truncate tracking-[0.4px]">{run.repo}</span>
          <span aria-hidden="true">/</span>
          <span className="shrink-0">{ago(run.updated_at, now)}</span>
        </p>
        {merged ? (
          <span
            aria-hidden="true"
            style={MERGE_PILL_WIDTH}
            className="h-11 min-w-11 font-mono text-micro"
          />
        ) : (
          <span className="relative flex">
            <SmallPill
              kind="primary"
              width={MERGE_PILL_WIDTH}
              label={pillLabel}
              tabIndex={stop === "pill" ? 0 : -1}
              {...cellAttrs(run.run, "pill")}
              aria-disabled={ariaDisabled || undefined}
              aria-label={pillName}
              aria-describedby={
                reason !== undefined && (blockedControl || state !== undefined)
                  ? reasonId
                  : undefined
              }
              onClick={onPill}
            />
          </span>
        )}
      </div>
      {/* The title and reason take the row's width less the chevron and its 13px gap; the chevron's
          tip sits on the content's right edge, its top at the title's first baseline. */}
      <div className="mt-[9.7px] flex gap-3.25 @min-[23.5rem]:-mt-[4.4px]">
        <div className="min-w-0 flex-1">
          <p
            id={titleId}
            className="line-clamp-2 text-head leading-5.5 font-semibold wrap-break-word text-ink @min-[23.5rem]:line-clamp-none"
          >
            {title}
          </p>
          {reason !== undefined && (
            <p id={reasonId} className={`mt-[4.5px] text-label leading-4 wrap-break-word ${tone}`}>
              {reason}
            </p>
          )}
        </div>
        <Chevron className="mt-[16.4px]" />
      </div>
    </li>
  );
}

// One opening of a row's dialog. Each is a new object, so the same row opened again is a change.
type Opened = { run: InboxRun };

// Marks the dialogs the list opens, which hand the focus back to it when they close.
const LIST_DIALOG_ATTR = "data-inbox-dialog";
const listDialog = { [LIST_DIALOG_ATTR]: "" };

type DialogProps = {
  opened: Opened | null;
  onClose: () => void;
  onConfirm: (run: InboxRun) => void;
};

// MergeDialog confirms one merge. It opens when `opened` is set; MERGE PR hands the run to
// onConfirm and closes it, and Escape and BACK close it without one.
function MergeDialog({ opened, onClose, onConfirm }: DialogProps) {
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

  function confirm() {
    if (run !== null) {
      onConfirm(run);
    }
    dialogRef.current?.close();
  }

  const pull = run === null ? null : parsePull(run.pr_url);
  const checks = run?.checks;

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
    </dialog>,
    document.body,
  );
}

// One action of a row's sheet, a 68px row under a rule: the label at its 28px baseline, the detail
// at 48, the chevron from 30 to 42. A later action takes the same class and body on a button.
const sheetActionClass = "flex min-h-[67px] gap-3.5 rounded-tile pt-[11.9px] pb-4";

type SheetActionBodyProps = {
  label: string;
  detail: string;
};

function SheetActionBody({ label, detail }: SheetActionBodyProps) {
  return (
    <>
      <span className="min-w-0 flex-1">
        <span className="block text-body leading-5 wrap-break-word text-ink">{label}</span>
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
};

// RowSheet is a row's actions, opened by the row. It opens when `opened` is set with the focus on
// its first action, or on the close control when the row has no pull request to open; an action,
// the close control, Escape and a press on the scrim close it, and the dialog hands the focus back
// to the row.
function RowSheet({ opened, onClose }: SheetProps) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const firstRef = useRef<HTMLAnchorElement>(null);
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
          {pull === null ? (
            <p className="mt-[18.5px] border-t border-hair pt-3 text-label leading-5 text-muted">
              {run.pr_url === ""
                ? "This run has no pull request to open."
                : "This pull request's link cannot be opened here."}
            </p>
          ) : (
            <ul className="mt-[18.5px]">
              <li className="border-t border-hair">
                <a
                  ref={firstRef}
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
            </ul>
          )}
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

type Merges = {
  // Each row's merge state, keyed by run, so a response only ever writes its own row.
  rows: Record<string, RowState>;
  // The words the live region tells, so a row that leaves with its merge still announces it.
  announcement: string;
  merge: (run: InboxRun, sha: string) => Promise<void>;
  clearRefusal: (run: InboxRun) => void;
};

// useMerges holds the merges made from the list beside the read itself, so a merge in flight and
// the re-read after it outlive the Overview being closed and opened again.
function useMerges(refresh: () => void): Merges {
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

  return { rows, announcement, merge, clearRefusal };
}

type InboxProviderProps = {
  children: React.ReactNode;
};

/**
 * InboxProvider reads the inbox for every screen inside it and holds the merges made from it: the
 * menu counts the actions and the Overview lists them.
 */
export function InboxProvider({ children }: InboxProviderProps) {
  const read = useInboxRead();
  const merges = useMerges(read.refresh);
  return <InboxContext value={{ ...read, ...merges }}>{children}</InboxContext>;
}

// Where focus sits in the list: a row's run, and which of its cells.
type Position = { run: string; cell: Cell };

function positionOf(target: EventTarget | null): Position | null {
  if (!(target instanceof HTMLElement)) {
    return null;
  }
  const run = target.getAttribute(RUN_ATTR);
  const cell = target.getAttribute(CELL_ATTR);
  if (run === null || (cell !== "row" && cell !== "pill")) {
    return null;
  }
  return { run, cell };
}

function cellIn(list: HTMLElement | null, run: string, cell: Cell): HTMLElement | null {
  return (
    list?.querySelector<HTMLElement>(
      `[${RUN_ATTR}="${CSS.escape(run)}"][${CELL_ATTR}="${cell}"]`,
    ) ?? null
  );
}

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
