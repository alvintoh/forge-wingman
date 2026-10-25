import { useId } from "react";

type RadioChoiceProps = {
  // The id of the element that names the choice.
  labelledBy: string;
  checked: boolean;
  onChoose: () => void;
  title?: string;
  // Where the bullet sits in the row, which is positioned.
  bulletClass: string;
};

/**
 * RadioChoice makes a whole row one radio: a button laid over the row, out to the card's edges and
 * under the row's own controls, which are positioned after it. Its bullet is a 20px ring, with a
 * 10px dot while chosen.
 *
 * Every radio is a tab stop and Space or Enter chooses it, with no arrow keys: a choice hides the
 * other rows, so the arrows could not reach them.
 */
export function RadioChoice({
  labelledBy,
  checked,
  onChoose,
  title,
  bulletClass,
}: RadioChoiceProps) {
  return (
    <>
      <button
        type="button"
        role="radio"
        aria-checked={checked}
        aria-labelledby={labelledBy}
        title={title}
        onClick={onChoose}
        className="absolute inset-y-0 -inset-x-padx cursor-pointer rounded-tile focus-visible:outline-offset-[-7.5px]!"
      />
      <span
        aria-hidden="true"
        className={`pointer-events-none absolute left-0 flex size-5 items-center justify-center rounded-full border-2 bg-card ${checked ? "border-accent" : "border-ink2"} ${bulletClass}`}
      >
        {checked && <span className="size-2.5 rounded-full bg-accent" />}
      </span>
    </>
  );
}

type AllProjectsRowProps = {
  checked: boolean;
  onChoose: () => void;
};

/** AllProjectsRow is the list's first radio: no project chosen. */
export function AllProjectsRow({ checked, onChoose }: AllProjectsRowProps) {
  const labelId = useId();
  return (
    <div className="relative flex min-h-13 items-center pl-8">
      <RadioChoice
        labelledBy={labelId}
        checked={checked}
        onChoose={onChoose}
        bulletClass="top-1/2 -translate-y-1/2"
      />
      <span id={labelId} className="text-body font-semibold text-ink">
        All projects
      </span>
    </div>
  );
}
