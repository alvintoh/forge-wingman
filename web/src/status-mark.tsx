export type StatusKind = "on" | "off" | "below" | "few";

/** StatusMark draws a status as a shape beside its word: a tick in a disc, a bar, an exclamation or a dot in a ring. */
export function StatusMark({ kind }: { kind: StatusKind }) {
  return (
    <svg aria-hidden="true" width="18" height="18" viewBox="0 0 18 18" className="shrink-0">
      {kind === "on" && (
        <>
          <circle cx="9" cy="9" r="9" className="fill-ok" />
          <path
            d="M3.5 9.5l4 4l7-8"
            fill="none"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            className="stroke-card"
          />
        </>
      )}
      {kind === "off" && (
        <>
          <circle cx="9" cy="9" r="8" fill="none" strokeWidth="2" className="stroke-ink2" />
          <rect x="4" y="8" width="10" height="2" className="fill-ink2" />
        </>
      )}
      {kind === "below" && (
        <>
          <circle cx="9" cy="9" r="8" fill="none" strokeWidth="2" className="stroke-defer" />
          <rect x="8" y="4" width="2" height="6" className="fill-defer" />
          <circle cx="9" cy="13" r="1.2" className="fill-defer" />
        </>
      )}
      {kind === "few" && (
        <>
          <circle cx="9" cy="9" r="8" fill="none" strokeWidth="2" className="stroke-ink2" />
          <circle cx="9" cy="9" r="1.5" className="fill-ink2" />
        </>
      )}
    </svg>
  );
}
