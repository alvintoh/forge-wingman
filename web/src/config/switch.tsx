import { useId } from "react";

type SwitchProps = {
  // Null while the saved value is unread: the switch's place is held and nothing is offered.
  on: boolean | null;
  // The ids of the heading that names the setting and of the sentence that says what it does.
  labelledBy: string;
  describedBy: string;
  disabled: boolean;
  onToggle: () => void;
};

/**
 * Switch is a setting's on/off control: the word On or Off, then a 44x26 track in a 56x44 target
 * whose visible track ends on the card's text edge. The state is the knob's place and the word,
 * never the track's colour alone.
 */
export function Switch({ on, labelledBy, describedBy, disabled, onToggle }: SwitchProps) {
  if (on === null) {
    return <span aria-hidden="true" className="-mr-1.5 h-11 w-[5.25rem] shrink-0" />;
  }
  return (
    <div className="flex shrink-0 items-center gap-1">
      <span aria-hidden="true" className="w-6 text-right text-label font-semibold text-ink">
        {on ? "On" : "Off"}
      </span>
      <button
        type="button"
        role="switch"
        aria-checked={on}
        aria-labelledby={labelledBy}
        aria-describedby={describedBy}
        disabled={disabled}
        onClick={onToggle}
        className="group -mr-1.5 inline-flex h-11 w-14 cursor-pointer items-center justify-center focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-60"
      >
        <span
          className={`relative h-6.5 w-11 rounded-full group-focus-visible:outline-3 group-focus-visible:outline-offset-2 group-focus-visible:outline-accent ${on ? "bg-accent" : "bg-card ring-2 ring-ink2 ring-inset"}`}
        >
          <span
            className={`absolute top-1 size-4.5 rounded-full transition-[left] duration-150 motion-reduce:transition-none ${on ? "left-5.5 bg-card" : "left-1 bg-ink2"}`}
          />
        </span>
      </button>
    </div>
  );
}

type SwitchRowProps = {
  name: string;
  // The heading's level: a section's own switch is its h2, a switch inside a section an h3.
  level: "h2" | "h3";
  on: boolean | null;
  disabled: boolean;
  onToggle: () => void;
  // The sentence on the shown value, or the words for a value still unread or unreadable.
  children: React.ReactNode;
};

/** SwitchRow is a setting's name with its switch at the right, then the sentence under it. */
export function SwitchRow({ name, level, on, disabled, onToggle, children }: SwitchRowProps) {
  const id = useId();
  const Heading = level;
  return (
    <>
      <div className="flex min-h-11 items-center justify-between gap-3">
        <Heading id={`${id}-name`} className="min-w-0 text-body font-semibold text-ink">
          {name}
        </Heading>
        <Switch
          on={on}
          labelledBy={`${id}-name`}
          describedBy={`${id}-says`}
          disabled={disabled}
          onToggle={onToggle}
        />
      </div>
      <div id={`${id}-says`} className="text-label leading-5 text-ink">
        {children}
      </div>
    </>
  );
}
