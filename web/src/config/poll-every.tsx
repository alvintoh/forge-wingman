import type { KeyboardEvent, MouseEvent } from "react";
import { POLL_CHOICES, isPollChoice } from "./types";

type SegmentProps = {
  minutes: number;
  checked: boolean;
  // Whether this segment takes the group's tab stop: the checked one, or the first while none is.
  tabStop: boolean;
  first: boolean;
  disabled: boolean;
  onClick: (event: MouseEvent<HTMLButtonElement>) => void;
  onKeyDown: (event: KeyboardEvent<HTMLButtonElement>) => void;
};

/**
 * Segment is one choice: visibly 106x26 inside the unit's outline, in a 106x44 target. The checked
 * one is filled accent with a tick before its label, so the choice is never told by colour alone;
 * while the control is inactive it keeps its tick in muted, on the unit's track.
 */
function Segment({ minutes, checked, tabStop, first, disabled, onClick, onKeyDown }: SegmentProps) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={checked}
      data-minutes={minutes}
      tabIndex={tabStop ? 0 : -1}
      disabled={disabled}
      onClick={onClick}
      onKeyDown={onKeyDown}
      className="group relative flex h-11 w-26.5 cursor-pointer items-center focus-visible:outline-none! disabled:cursor-not-allowed"
    >
      {/* The fill sits inside the unit's 1px edge, so the edge stays whole. */}
      <span
        aria-hidden="true"
        className={`absolute inset-y-2.5 ${first ? "right-0 left-px rounded-l-[5px]" : "right-px left-0 rounded-r-[5px]"} ${checked && !disabled ? "bg-accent" : ""} group-focus-visible:outline-3 group-focus-visible:outline-offset-2 group-focus-visible:outline-accent`}
      />
      <span
        className={`relative flex w-full items-center justify-center text-label font-semibold ${disabled ? "text-muted" : checked ? "text-card" : "text-ink"}`}
      >
        {checked && (
          <svg
            aria-hidden="true"
            width="8.5"
            height="6.5"
            viewBox="0 0 8.5 6.5"
            className="absolute left-3 overflow-visible"
          >
            <path
              d="M0 3.5l3 3l5.5 -6.5"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        )}
        <span className={checked ? "pl-3.5" : ""}>{minutes} minutes</span>
      </span>
    </button>
  );
}

type PollEveryProps = {
  // The id of the label that names the choice.
  labelledBy: string;
  // The minutes the control shows, null until the saved interval is read.
  value: number | null;
  disabled: boolean;
  onChoose: (minutes: number) => void;
};

/**
 * PollEvery is the owner's poll interval as one outlined two-segment unit, visibly 212x26 like the
 * stepper's height, split by a divider. It follows the radio group pattern: one tab stop, and the
 * arrow keys move the choice. Choosing stages the interval; the page's SAVE saves it. While the
 * value is unread it is inactive with nothing chosen.
 */
export function PollEvery({ labelledBy, value, disabled, onChoose }: PollEveryProps) {
  const inactive = disabled || value === null;
  const ticked = isPollChoice(value);

  // minutesOf is the choice a segment's button holds.
  function minutesOf(button: HTMLButtonElement): number {
    return Number(button.dataset.minutes);
  }

  function onClick(event: MouseEvent<HTMLButtonElement>) {
    onChoose(minutesOf(event.currentTarget));
  }

  function onKeyDown(event: KeyboardEvent<HTMLButtonElement>) {
    const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[event.key];
    if (step === undefined) {
      return;
    }
    event.preventDefault();
    const at = POLL_CHOICES.findIndex((m) => m === minutesOf(event.currentTarget));
    const next = POLL_CHOICES[(at + step + POLL_CHOICES.length) % POLL_CHOICES.length];
    onChoose(next);
    event.currentTarget.parentElement
      ?.querySelector<HTMLButtonElement>(`[data-minutes="${next}"]`)
      ?.focus();
  }

  return (
    <div
      role="radiogroup"
      aria-labelledby={labelledBy}
      aria-disabled={inactive}
      className="relative flex shrink-0"
    >
      <span
        aria-hidden="true"
        className={`pointer-events-none absolute inset-x-0 top-1/2 h-6.5 -translate-y-1/2 rounded-control border ${inactive ? "border-track bg-track" : "border-ink2 bg-card"}`}
      />
      {POLL_CHOICES.map((minutes, k) => (
        <Segment
          key={minutes}
          minutes={minutes}
          checked={minutes === value}
          tabStop={ticked ? minutes === value : k === 0}
          first={k === 0}
          disabled={inactive}
          onClick={onClick}
          onKeyDown={onKeyDown}
        />
      ))}
      {!inactive && (
        <span
          aria-hidden="true"
          className="pointer-events-none absolute top-1/2 left-1/2 h-6 w-px -translate-x-1/2 -translate-y-1/2 bg-ink2"
        />
      )}
    </div>
  );
}
