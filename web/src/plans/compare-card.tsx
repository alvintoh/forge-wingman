import { captionClass } from "../caption";
import type { Column, Piece } from "./compare";

// The card's title, and the phone's heading over its cards; the page holds one, so they are fixed.
const TITLE_ID = "compare-card-title";

// Every @[47rem] below is the width of the column the cards sit in from which the plans draw as
// one table, and under it as one card per plan. 47rem less the card's 26px sides is the 700px the
// loading card's table is drawn at (43.75rem): a 176px label column and two plan columns of 262px.

const sectionTitleClass = "text-cap font-semibold leading-4 tracking-[1.2px] text-ink2 uppercase";
const labelClass = "text-label font-semibold leading-4.5 text-ink2";

function Glyph({ glyph }: { glyph: "yes" | "no" | "unconfirmed" }) {
  return (
    <svg
      aria-hidden="true"
      width="12"
      height="12"
      viewBox="0 0 12 12"
      fill="none"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="mt-0.75 shrink-0 stroke-ink"
    >
      {glyph === "yes" && <path d="M1.75 6.25l3 3l5.5 -6.5" />}
      {glyph === "no" && <path d="M2.5 2.5l7 7M9.5 2.5l-7 7" />}
      {glyph === "unconfirmed" && (
        <circle
          cx="6"
          cy="6"
          r="5.25"
          strokeWidth="1.5"
          strokeDasharray="2.5 2"
          className="stroke-ink2"
        />
      )}
    </svg>
  );
}

// Exact is a solid outline and a tick, estimated a dashed one and a wave, so the two never differ by colour.
function MarkChip({ mark }: { mark: "exact" | "estimated" }) {
  const estimated = mark === "estimated";
  return (
    <span
      className={`mb-2 inline-flex h-5.5 items-center gap-1.5 self-start rounded-full border-[1.5px] border-ink2 px-2.5 font-mono text-micro font-semibold tracking-[1.2px] text-ink uppercase ${estimated ? "border-dashed" : ""}`}
    >
      <svg
        aria-hidden="true"
        width="12"
        height="12"
        viewBox="0 0 12 12"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
        className="shrink-0"
      >
        {estimated ? (
          <path d="M3 4.5q1.5 -2 3 0t3 0M3 8.5q1.5 -2 3 0t3 0" />
        ) : (
          <path d="M1.5 6.5l3 3l6 -7" />
        )}
      </svg>
      {mark}
    </span>
  );
}

function PieceView({ piece }: { piece: Piece }) {
  switch (piece.kind) {
    case "text":
      return <p className="text-label leading-4.5 text-ink">{piece.text}</p>;
    case "label":
      return <p className={labelClass}>{piece.text}</p>;
    case "headline":
      return <p className="text-title leading-8.5 font-semibold text-ink">{piece.text}</p>;
    case "limit":
      return <p className="font-mono text-label leading-4.5 text-ink">{piece.text}</p>;
    case "requests":
      return <p className="font-mono text-cap leading-4 text-ink2">{piece.text}</p>;
    case "note":
      return <p className="text-cap leading-4 text-ink2">{piece.text}</p>;
    case "shape":
      return <p className="font-mono text-cap leading-4 text-ink2">{piece.text}</p>;
    case "figure":
      return (
        <p className="flex items-baseline gap-2">
          <span className="font-mono text-title leading-8.5 font-semibold text-ink">
            {piece.value}
          </span>
          <span className="text-label text-ink2">requests per $1</span>
        </p>
      );
    case "peak":
      return (
        <p className="text-label leading-5.5 text-ink2">
          <span className="font-mono text-ink">{piece.value}</span> {piece.words}
        </p>
      );
    case "mark":
      return <MarkChip mark={piece.mark} />;
    case "list":
      return (
        <ul>
          {piece.items.map((item, at) => (
            <li key={`${at}-${item}`} className="relative pl-3.5 text-label leading-4.5 text-ink">
              <span
                aria-hidden="true"
                className="absolute top-1.75 left-px size-1 rounded-full bg-ink2"
              />
              {item}
            </li>
          ))}
        </ul>
      );
    case "glyph":
      return (
        <p
          className={`flex gap-1.5 text-label leading-4.5 ${piece.glyph === "unconfirmed" ? "text-ink2" : "text-ink"}`}
        >
          <Glyph glyph={piece.glyph} />
          {piece.word}
        </p>
      );
  }
}

function Cell({ pieces }: { pieces: readonly Piece[] }) {
  return (
    <div className="flex min-w-0 flex-col break-words">
      {pieces.map((piece, at) => (
        <PieceView key={`${at}-${piece.kind}`} piece={piece} />
      ))}
    </div>
  );
}

type CompareCardProps = {
  columns: readonly Column[];
};

// The heading's id is the card's place, never the plan's id, which the server writes and may hold a space.
function PlanCard({ column, at }: { column: Column; at: number }) {
  const nameId = `compare-plan-${at}`;
  return (
    <section aria-labelledby={nameId} className="card-frame pt-6 pb-5.5 max-sm:-mx-2.5">
      <h3 id={nameId} className="min-w-0 text-head leading-6 font-semibold break-words text-ink">
        {column.name}
      </h3>
      <p className="min-w-0 font-mono text-cap leading-4 break-words text-ink2">{column.id}</p>
      {column.sections.map((section) => (
        <div key={section.title} className="mt-3 border-t border-hair pt-3">
          <h4 className={sectionTitleClass}>{section.title}</h4>
          <dl>
            {section.rows.map((row) => (
              <div key={row.label} className="mt-3.5">
                <dt className={labelClass}>{row.label}</dt>
                <dd className="mt-0.5">
                  <Cell pieces={row.pieces} />
                </dd>
              </div>
            ))}
          </dl>
        </div>
      ))}
    </section>
  );
}

/**
 * CompareCard draws the plans side by side: from a wide column one card holding a table, a
 * column per plan under its name and id; narrower, one card per plan with the same rows and no
 * table. Both are in the markup and the container query shows one, so no script decides it. The
 * label column is the rows' own, not sticky: the table fits its two plans, and a wider one is
 * for the card that scrolls it.
 */
export function CompareCard({ columns }: CompareCardProps) {
  const [first] = columns;
  if (first === undefined) {
    return null;
  }
  return (
    <div className="@container flex min-w-0 flex-col gap-y-2">
      <h2 className="sr-only @[47rem]:hidden">plans side by side</h2>
      <div className="flex min-w-0 flex-col gap-y-2 @[47rem]:hidden">
        {columns.map((column, at) => (
          <PlanCard key={`${at}-${column.id}`} column={column} at={at} />
        ))}
      </div>
      <section aria-labelledby={TITLE_ID} className="card-frame hidden pt-6 pb-4 @[47rem]:block">
        <h2 id={TITLE_ID} className={`${captionClass} leading-5 text-ink2`}>
          plans side by side
        </h2>
        <table className="mt-3 w-full table-fixed border-collapse text-left">
          <colgroup>
            <col className="w-44" />
            {columns.map((column, at) => (
              <col key={`${at}-${column.id}`} />
            ))}
          </colgroup>
          <thead>
            <tr className="border-b border-hair">
              <td />
              {columns.map((column, at) => (
                <th
                  key={`${at}-${column.id}`}
                  scope="col"
                  className="h-15 pr-4 pb-3 align-top font-normal"
                >
                  <span className="block min-w-0 pt-4 text-body leading-5 font-semibold break-words text-ink">
                    {column.name}
                  </span>
                  <span className="block font-mono text-cap leading-4 break-words text-ink2">
                    {column.id}
                  </span>
                </th>
              ))}
            </tr>
          </thead>
          {first.sections.map((section, sectionAt) => {
            const lastSection = sectionAt === first.sections.length - 1;
            return (
              <tbody key={section.title}>
                <tr>
                  <th
                    scope="rowgroup"
                    colSpan={columns.length + 1}
                    className={`h-8.5 pt-3.5 text-left align-top ${sectionTitleClass}`}
                  >
                    {section.title}
                  </th>
                </tr>
                {section.rows.map((row, rowAt) => {
                  const last = lastSection && rowAt === section.rows.length - 1;
                  return (
                    <tr key={row.label} className={last ? "" : "border-b border-hair"}>
                      <th scope="row" className={`py-3 pr-4 text-left align-top ${labelClass}`}>
                        {row.label}
                      </th>
                      {columns.map((column, at) => {
                        return (
                          <td key={`${at}-${column.id}`} className="py-3 pr-4 align-top">
                            <Cell pieces={column.sections[sectionAt].rows[rowAt].pieces} />
                          </td>
                        );
                      })}
                    </tr>
                  );
                })}
              </tbody>
            );
          })}
        </table>
      </section>
    </div>
  );
}
