import type { ReactNode } from "react";
import { captionClass } from "../caption";
import { useWidth } from "./use-width";

// Under this width a chart has no room to be drawn, so none is, as a card measured
// mid-layout would otherwise draw one.
const MIN_CHART_W = 240;

type ChartCardProps = {
  // Under this width nothing is drawn, not even the card.
  minWidth?: number;
  children: (width: number) => ReactNode;
};

// ChartCard is one chart's card. It measures its own width and hands it to the
// chart, which derives its geometry from it: one component at every width, never
// two drawn variants. On a phone it is 10px wider each side than the page's text,
// as the drawing's phone cards are, and nothing is drawn until it is measured.
export function ChartCard({ minWidth = MIN_CHART_W, children }: ChartCardProps) {
  const { ref, width } = useWidth<HTMLDivElement>();
  return (
    <div ref={ref} className="max-sm:-mx-2.5">
      {width >= minWidth && (
        <div className="card-frame py-3">
          {/* The card's own width inside its 1px edge, which the chart fills. */}
          <div className="-mx-padx">{children(width - 2)}</div>
        </div>
      )}
    </div>
  );
}

type ChartHeaderProps = {
  title: string;
  aside?: string;
  padx: number;
};

// ChartHeader is the line above a chart: its caption, and a quieter one at the right.
export function ChartHeader({ title, aside, padx }: ChartHeaderProps) {
  return (
    <div
      className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1"
      style={{ paddingInline: padx }}
    >
      <h3 className={`${captionClass} text-ink2`}>{title}</h3>
      {aside !== undefined && <p className="text-cap text-muted">{aside}</p>}
    </div>
  );
}
