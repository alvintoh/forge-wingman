type DisclosureRowProps = {
  open: boolean;
  // The row's words while closed; it reads "Show fewer" while open.
  label: string;
  onClick: () => void;
  // The row's indent, where the rows above it are indented past a mark.
  className?: string;
};

/**
 * DisclosureRow is the accent, underlined 52px row that opens a list's rest in place and closes it
 * again: the filter column's projects, and the Overview's runs in flight.
 */
export function DisclosureRow({ open, label, onClick, className = "" }: DisclosureRowProps) {
  return (
    <div className={`border-t border-hair ${className}`}>
      <button
        type="button"
        aria-expanded={open}
        onClick={onClick}
        className="flex min-h-13 w-fit items-center text-label font-semibold text-accent underline"
      >
        {open ? "Show fewer" : label}
      </button>
    </div>
  );
}
