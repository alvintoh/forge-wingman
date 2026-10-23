import { useId } from "react";
import { ago } from "../ago";
import { pillSetWidth } from "../button-style";
import { SmallPill } from "../pill-button";
import { Chevron } from "./chevron";
import type { CancelState } from "./cancel-state";
import { type Cell, cellAttrs } from "./keyboard";
import type { RowState } from "./merge-state";
import { type InboxRun, isStopped, stopReasonOf, titleOf } from "./run";

type Kind = "FORK" | "TODO" | "REVIEW" | "STOPPED";

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
  STOPPED: "bg-stop",
};

const UNREAD_WORDS = "GitHub could not be read";
const MERGING_WORDS = "squash in progress";
const MERGED_WORDS = "Merged. Squashed, branch deleted.";

// The merge pill takes RETRY MERGE's width, the set the drawing sizes it from; a stopped run's RETRY
// pill takes the same width.
const MERGING_LABEL = "MERGING...";
const MERGE_PILL_WIDTH = pillSetWidth(["MERGE PR", MERGING_LABEL, "RETRY MERGE"]);

// Under 17rem (272px) of row the chip, the ticket and the pill (about 252px with the widest chip)
// no longer fit one line, so the pill takes a line of its own under the meta line.
const pillSlot =
  "col-span-2 row-start-3 justify-self-end @min-[17rem]:col-span-1 @min-[17rem]:col-start-2 @min-[17rem]:row-start-1 @min-[17rem]:justify-self-auto @min-[23.5rem]:col-start-3";

const chipClass =
  "inline-flex h-5.5 shrink-0 items-center rounded-full px-2.5 font-mono text-micro font-semibold tracking-[1.2px] text-card";

type RowProps = {
  run: InboxRun;
  now: number;
  state: RowState | undefined;
  // What a cancel from the row's sheet is doing, or why it did not go through.
  cancel: CancelState | undefined;
  // Which of the row's cells is the list's one tab stop, if the stop is on this row.
  stop: Cell | undefined;
  onOpen: (run: InboxRun) => void;
  onMerge: (run: InboxRun) => void;
  onRetry: (run: InboxRun, sha: string) => void;
  onCompose: (run: InboxRun) => void;
};

export function InboxRow({
  run,
  now,
  state,
  cancel,
  stop,
  onOpen,
  onMerge,
  onRetry,
  onCompose,
}: RowProps) {
  const reasonId = useId();
  const ticketId = useId();
  const titleId = useId();
  const wait: Wait | undefined = waits[run.wait_reason];
  const draft = run.draft === true;
  const blocked = wait?.blocks === true || draft;
  const unread = run.head_sha === undefined;
  const title = titleOf(run);
  const stopped = isStopped(run);
  // A draft on GitHub shows the same chip as a stored draft reason, whatever reason is stored.
  const kind: Kind = stopped ? "STOPPED" : draft ? "TODO" : (wait?.kind ?? "REVIEW");

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
  // A stopped run waits for a retry, not a merge: its reason is why it stopped, whatever PR it names.
  if (stopped) {
    reason = stopReasonOf(run);
    tone = "text-stop";
  }
  // A cancel says where it stands on its own row: under way, or why it did not go through.
  if (cancel !== undefined) {
    reason = cancel.status === "sending" ? "Cancelling…" : `Not cancelled. ${cancel.words}`;
    tone = cancel.status === "sending" ? "text-ink2" : "text-stop";
  }
  // A merged row has no pill, so a stop left on it falls back to the row.
  const rowStop = stop === "row" || (stop === "pill" && merged);

  function handleOpen() {
    onOpen(run);
  }

  function handleCompose() {
    onCompose(run);
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
      <div className="grid min-h-11 grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-2 @min-[17rem]:gap-y-0 @min-[23.5rem]:grid-cols-[auto_minmax(0,1fr)_auto] @min-[23.5rem]:gap-x-2">
        <div className="col-span-2 row-start-1 flex items-center gap-3 @min-[17rem]:col-span-1">
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
            className={`h-11 min-w-11 font-mono text-micro ${pillSlot}`}
          />
        ) : stopped ? (
          <span className={`relative flex ${pillSlot}`}>
            <SmallPill
              kind="secondary"
              width={MERGE_PILL_WIDTH}
              label="RETRY"
              tabIndex={stop === "pill" ? 0 : -1}
              {...cellAttrs(run.run, "pill")}
              aria-haspopup="dialog"
              aria-label={`Retry ${run.ticket} in ${run.repo} with a note`}
              onClick={handleCompose}
            />
          </span>
        ) : (
          <span className={`relative flex ${pillSlot}`}>
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
