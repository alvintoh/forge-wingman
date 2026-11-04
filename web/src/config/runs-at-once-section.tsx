import type { KeyboardEvent } from "react";
import type { Read } from "../stats/use-read";
import { MIN_RUNS_AT_ONCE, noteWords } from "./runs-at-once";
import type { RunsAtOnceView } from "./runs-at-once";

const TITLE_ID = "config-runs-at-once";
const SENTENCE_ID = "config-runs-at-once-says";

type StepHalfProps = {
  sign: "+" | "−";
  name: string;
  live: boolean;
  disabled: boolean;
  onStep: () => void;
};

/**
 * StepHalf is one half of the stepper: visibly 24x13, with a 24x24 tap area that extends past the
 * unit's top ("+") or bottom ("−") and meets the other half's at the seam.
 */
function StepHalf({ sign, name, live, disabled, onStep }: StepHalfProps) {
  const top = sign === "+";
  return (
    <button
      type="button"
      tabIndex={-1}
      aria-label={name}
      disabled={!live || disabled}
      onClick={onStep}
      className={`flex h-6 w-full cursor-pointer disabled:cursor-not-allowed ${top ? "items-end" : "items-start"}`}
    >
      <span
        aria-hidden="true"
        className={`mr-px flex h-3 flex-1 items-center justify-center ${top ? "rounded-tr-control" : "rounded-br-control border-t"} ${disabled ? "border-track" : "border-hair"} ${live || disabled ? "" : "bg-track"}`}
      >
        <svg viewBox="0 0 7 7" className="size-1.75" fill="none">
          <path
            d={top ? "M0 3.5h7M3.5 0v7" : "M0 3.5h7"}
            strokeWidth="1.5"
            strokeLinecap="round"
            className={live && !disabled ? "stroke-ink" : "stroke-muted"}
          />
        </svg>
      </span>
    </button>
  );
}

type StepperProps = {
  value: number;
  max: number;
  disabled: boolean;
  onChange: (n: number) => void;
};

/**
 * Stepper is N's control, visibly 56x26 like the switch's track: the value as a spinbutton, with
 * "+" stacked over "−" at its right. Arrow Up and Down on the value step it too.
 */
export function Stepper({ value, max, disabled, onChange }: StepperProps) {
  const canAdd = value < max;
  const canTake = value > MIN_RUNS_AT_ONCE;

  function add() {
    if (canAdd && !disabled) {
      onChange(value + 1);
    }
  }

  function take() {
    if (canTake && !disabled) {
      onChange(value - 1);
    }
  }

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === "ArrowUp") {
      event.preventDefault();
      add();
    } else if (event.key === "ArrowDown") {
      event.preventDefault();
      take();
    }
  }

  return (
    <div
      className={`flex h-6.5 w-14 shrink-0 rounded-control border ${disabled ? "border-track bg-track" : "border-ink2 bg-card"}`}
    >
      <div
        role="spinbutton"
        tabIndex={0}
        aria-labelledby={TITLE_ID}
        aria-describedby={SENTENCE_ID}
        aria-valuemin={MIN_RUNS_AT_ONCE}
        aria-valuemax={max}
        aria-valuenow={value}
        aria-disabled={disabled}
        onKeyDown={onKeyDown}
        className={`flex w-7.75 items-center justify-center rounded-l-control border-r text-body font-semibold focus-visible:outline-3 focus-visible:outline-offset-2 focus-visible:outline-accent ${disabled ? "border-track text-muted" : "border-ink2 text-ink"}`}
      >
        {value}
      </div>
      {/* The halves' column overlaps the unit's top, bottom and right edges by their tap areas. */}
      <div className="-mt-3 -mr-px flex h-12 flex-1 flex-col">
        <StepHalf
          sign="+"
          name="More runs at once"
          live={canAdd}
          disabled={disabled}
          onStep={add}
        />
        <StepHalf
          sign="−"
          name="Fewer runs at once"
          live={canTake}
          disabled={disabled}
          onStep={take}
        />
      </div>
    </div>
  );
}

type RunsAtOnceSectionProps = {
  read: Read<RunsAtOnceView>;
  // The saved N's view: the read's answer, or the answer to the last save.
  view: RunsAtOnceView | null;
  // The N the stepper shows, null until N is read.
  value: number | null;
  disabled: boolean;
  onChange: (n: number) => void;
};

/**
 * RunsAtOnceSection is the owner's N: the name with its stepper, what N is, then the N the
 * concurrency rule recommends and why. The stepper saves nothing; the page's one SAVE saves it.
 */
export function RunsAtOnceSection({
  read,
  view,
  value,
  disabled,
  onChange,
}: RunsAtOnceSectionProps) {
  const r = view?.recommendation ?? null;
  return (
    <section
      aria-labelledby={TITLE_ID}
      aria-busy={read.status === "loading"}
      className="border-t border-hair pt-3.5 pb-5 text-label leading-5"
    >
      <div className="flex min-h-11 items-center justify-between gap-3">
        <h2 id={TITLE_ID} className="min-w-0 text-body font-semibold text-ink">
          Runs at once (N)
        </h2>
        {view !== null && value !== null && (
          <Stepper value={value} max={view.max} disabled={disabled} onChange={onChange} />
        )}
      </div>
      {read.status === "loading" && (
        <p role="status" className="text-ink2">
          Reading runs at once…
        </p>
      )}
      {read.status === "error" && (
        <p role="alert" className="text-ink2">
          Runs at once could not be read.
        </p>
      )}
      {view !== null && (
        <>
          <p id={SENTENCE_ID} className="mt-px text-ink">
            The most runs in flight at once, across every project.
          </p>
          <p className="mt-0.75 leading-4.5 text-ink2">
            {r === null
              ? "The recommendation could not be read."
              : noteWords(r, view.runs_at_once, view.max)}
          </p>
        </>
      )}
    </section>
  );
}
