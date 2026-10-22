import { kindClass, type PillWidth } from "./button-style";

type SmallPillProps = React.ComponentProps<"button"> & {
  label: string;
  kind: "primary" | "secondary";
  width: PillWidth;
};

/** TryAgainPill is the outlined TRY AGAIN pill a failed read offers, with a 44px hit area around it. */
export function TryAgainPill(props: React.ComponentProps<"button">) {
  return (
    <button
      type="button"
      className="group inline-flex min-h-11 min-w-11 shrink-0 cursor-pointer items-center justify-center disabled:cursor-not-allowed focus-visible:outline-none!"
      {...props}
    >
      <span className="inline-flex h-8 min-w-26 items-center justify-center rounded-full border border-accent px-3 font-mono text-micro font-semibold text-accent group-disabled:border-muted group-disabled:text-muted group-focus-visible:outline-3 group-focus-visible:outline-offset-2 group-focus-visible:outline-accent">
        TRY AGAIN
      </span>
    </button>
  );
}

/**
 * SmallPill is the design's small pill: 28px visible inside a 44px-high hit area, outlined in ink2
 * with an ink label (secondary) or filled accent with a white one (primary), and on track with a
 * muted label while disabled. The label's trailing tracking is cancelled (-mr) so the glyphs, not
 * the glyphs plus 1.2px, sit centred.
 */
export function SmallPill({ label, kind, width, ...rest }: SmallPillProps) {
  return (
    <button
      type="button"
      className="group inline-flex min-h-11 min-w-11 shrink-0 cursor-pointer items-center justify-center disabled:cursor-not-allowed aria-disabled:cursor-not-allowed focus-visible:outline-none!"
      {...rest}
    >
      <span
        style={width}
        className={`inline-flex h-7 items-center justify-center rounded-full border font-mono text-micro font-semibold tracking-[1.2px] group-disabled:border-hair group-disabled:bg-track group-disabled:text-muted group-aria-disabled:border-hair group-aria-disabled:bg-track group-aria-disabled:text-muted group-focus-visible:outline-3 group-focus-visible:outline-offset-2 group-focus-visible:outline-accent ${kindClass[kind]}`}
      >
        <span className="-mr-[1.2px]">{label}</span>
      </span>
    </button>
  );
}
