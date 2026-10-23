type ChevronProps = {
  className: string;
};

// Chevron is the drawing's row affordance: a 6x12 right chevron, muted, its tip at the box's 7px.
export function Chevron({ className }: ChevronProps) {
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
