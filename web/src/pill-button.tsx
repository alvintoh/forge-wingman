type PillButtonProps = React.ComponentProps<"button"> & { label: string };

/** A small outlined pill with a 44px hit area around it: every pair button in the app. */
export function PillButton({ label, ...rest }: PillButtonProps) {
  return (
    <button
      type="button"
      className="group inline-flex min-h-11 min-w-11 shrink-0 cursor-pointer items-center justify-center disabled:cursor-not-allowed"
      {...rest}
    >
      <span className="inline-flex h-8 min-w-26 items-center justify-center rounded-full border border-accent px-3 font-mono text-micro font-semibold text-accent group-disabled:border-muted group-disabled:text-muted">
        {label}
      </span>
    </button>
  );
}
