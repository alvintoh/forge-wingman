import { useId } from "react";
import { type InboxRun, stopReasonOf } from "./run";
import { type DetailSpan, spansOf, stopDetailOf } from "./stop-detail";

// From the drawing: the detail is the caption step in INK2, its mono spans in INK at the same size,
// its first baseline 20 under the stop reason's; a mono span wider than the line breaks anywhere.
const paragraphClass = "text-cap leading-4 wrap-break-word text-ink2";

function Spans({ spans }: { spans: DetailSpan[] }) {
  return spans.map((span, index) =>
    span.mono ? (
      <code key={index} className="font-mono wrap-anywhere text-ink">
        {span.text}
      </code>
    ) : (
      span.text
    ),
  );
}

type RowDetailProps = { id: string; paragraph: string };

// RowStopDetail is a stopped row's first paragraph, clamped to three lines; the rest is the sheet's.
export function RowStopDetail({ id, paragraph }: RowDetailProps) {
  return (
    <p id={id} className={`mt-[4.4px] line-clamp-3 ${paragraphClass}`}>
      <Spans spans={spansOf(paragraph)} />
    </p>
  );
}

// SheetStopDetail is a stopped run's reason and its whole detail, every paragraph 8px apart, and
// nothing for a run with no detail. Past 14 lines the detail scrolls in place, so the sheet's actions
// stay on the screen; the scroller is a region named by the reason and takes the focus, so a keyboard
// can scroll it.
export function SheetStopDetail({ run }: { run: InboxRun }) {
  const reasonId = useId();
  const paragraphs = stopDetailOf(run);
  if (paragraphs.length === 0) {
    return null;
  }
  return (
    <>
      <p id={reasonId} className="mt-[9.6px] text-label leading-4 wrap-break-word text-stop">
        {stopReasonOf(run)}
      </p>
      <div
        role="region"
        aria-labelledby={reasonId}
        tabIndex={0}
        className="mt-[4.4px] max-h-56 space-y-2 overflow-y-auto rounded-tile"
      >
        {paragraphs.map((paragraph, index) => (
          <p key={index} className={paragraphClass}>
            <Spans spans={spansOf(paragraph)} />
          </p>
        ))}
      </div>
    </>
  );
}
