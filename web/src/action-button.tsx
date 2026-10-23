import { disabledEdge, kindClass } from "./button-style";

type ActionButtonProps = React.ComponentProps<"button"> & {
  label: string;
  kind: "primary" | "secondary";
};

/**
 * ActionButton is the app's one 44px button: a dialog's pair and a menu's
 * actions. It hugs its label with 20px either side and never goes under the
 * width "MERGE PR" gives it (102.4px), so a short label reads as the same
 * button; a pair or a stack sets its width from outside.
 */
export function ActionButton({ label, kind, ...rest }: ActionButtonProps) {
  return (
    <button
      type="button"
      className={`inline-flex h-11 min-w-[102.4px] cursor-pointer items-center justify-center rounded-full border px-5 font-mono text-micro font-semibold tracking-[1.2px] disabled:cursor-not-allowed disabled:bg-track disabled:text-muted aria-disabled:cursor-not-allowed aria-disabled:bg-track aria-disabled:text-muted ${kindClass[kind]} ${disabledEdge[kind].self}`}
      {...rest}
    >
      {/* The tracking trails the last letter too; taking it back centres the ink. */}
      <span className="-mr-[1.2px]">{label}</span>
    </button>
  );
}

type ButtonPairProps = {
  back: React.ReactNode;
  action: React.ReactNode;
  margin?: string;
};

/**
 * ButtonPair lays out a dialog's BACK and its action. Under 600px they stack
 * at the full width, the action on top; from 600px they sit side by side,
 * centred, one width (the wider label's) and 72px apart. margin is the spacing
 * above the pair: 24px unless the caller sets another.
 */
export function ButtonPair({ back, action, margin = "mt-6" }: ButtonPairProps) {
  return (
    <div
      className={`${margin} grid gap-3 [&>:last-child]:order-first min-[600px]:mx-auto min-[600px]:w-max min-[600px]:auto-cols-fr min-[600px]:grid-flow-col min-[600px]:gap-18 min-[600px]:[&>:last-child]:order-none`}
    >
      {back}
      {action}
    </div>
  );
}
