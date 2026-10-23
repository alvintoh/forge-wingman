import type { TrendDirection } from "./types";
import { TRIANGLE_R } from "./axis";

// The ids of the two fills that tell the owner-line split's segments apart with
// no colour: the defs below define them and every bar draws them.
const HATCH_ID = "rewrite-hatch";
const DOTS_ID = "rewrite-dots";

export type SplitSegment = "review-found" | "owner-change" | "unlabelled";

const PATTERN_FILL = {
  "owner-change": `url(#${HATCH_ID})`,
  unlabelled: `url(#${DOTS_ID})`,
};

// PatternDefs holds the two patterns once for the whole section. It takes no
// room, and sits in the page so every chart's url(#id) finds it.
export function PatternDefs() {
  return (
    <svg width="0" height="0" aria-hidden="true" className="absolute">
      <defs>
        <pattern
          id={HATCH_ID}
          width="6"
          height="6"
          patternUnits="userSpaceOnUse"
          patternTransform="rotate(45)"
        >
          <rect width="6" height="6" className="fill-card" />
          <line x1="3" y1="0" x2="3" y2="6" strokeWidth="3" className="stroke-accent" />
        </pattern>
        <pattern id={DOTS_ID} width="4" height="4" patternUnits="userSpaceOnUse">
          <rect width="4" height="4" className="fill-card" />
          <circle cx="2" cy="2" r="1.5" className="fill-accent" />
        </pattern>
      </defs>
    </svg>
  );
}

type SegmentProps = {
  kind: SplitSegment;
  x: number | string;
  y?: number;
  width: number | string;
  height: number;
};

// Segment is one part of the owner-line split: solid, hatched or dotted. The two
// patterned ones carry an accent edge so a thin one is still a bounded mark.
export function Segment({ kind, x, y = 0, width, height }: SegmentProps) {
  return (
    <rect
      x={x}
      y={y}
      width={width}
      height={height}
      fill={kind === "review-found" ? undefined : PATTERN_FILL[kind]}
      strokeWidth={kind === "review-found" ? undefined : 1}
      className={kind === "review-found" ? "fill-accent" : "stroke-accent"}
      data-segment={kind}
    />
  );
}

// Swatch is a segment's key in the words under a row.
export function Swatch({ kind }: { kind: SplitSegment }) {
  return (
    <svg width="10" height="10" aria-hidden="true" className="shrink-0 overflow-visible">
      <Segment kind={kind} x={0} width={10} height={10} />
    </svg>
  );
}

type TriangleProps = {
  cx: number;
  cy: number;
  r?: number;
  className: string;
} & DataProps;

// Triangle is the drawing's one mark for a value past an axis: it points up from
// a band or a bar's end, in its own size, so it cannot be read as a rate.
export function Triangle({ cx, cy, r = TRIANGLE_R, className, ...data }: TriangleProps) {
  const points = `${cx},${cy - r} ${cx - r},${cy + r - 1} ${cx + r},${cy + r - 1}`;
  return <polygon points={points} className={className} {...data} />;
}

// The chart values a mark carries on the page, so a check reads them off the DOM.
type DataProps = {
  "data-pr"?: number;
  "data-rate"?: number;
  "data-clipped"?: "";
  "data-mark"?: string;
};

type DotProps = { cx: number; cy: number; r: number } & DataProps;

export function Dot({ cx, cy, r, ...data }: DotProps) {
  return (
    <circle cx={cx} cy={cy} r={r} strokeWidth={1.5} className="fill-accent stroke-card" {...data} />
  );
}

export function Diamond({ cx, cy, r = 6 }: { cx: number | string; cy: number; r?: number }) {
  return (
    <svg x={cx} y={cy} overflow="visible" aria-hidden="true">
      <polygon
        points={`0,${-r} ${r},0 0,${r} ${-r},0`}
        strokeWidth={1}
        className="fill-ink stroke-card"
        data-mark="diamond"
      />
    </svg>
  );
}

type RefLineProps = { x1: number | string; x2: number | string; y: number; width?: number };

// RefLine is the one reference line, at the bar: dashed as well as coloured, and
// always beside the words that name it.
export function RefLine({ x1, x2, y, width = 2 }: RefLineProps) {
  return (
    <line
      x1={x1}
      x2={x2}
      y1={y}
      y2={y}
      strokeWidth={width}
      strokeDasharray="7 4"
      strokeLinecap="round"
      className="stroke-defer"
      data-mark="reference"
    />
  );
}

const ARROW_FILL: Record<TrendDirection, string> = {
  up: "fill-stop",
  down: "fill-ok",
  flat: "fill-ink",
};

// TrendArrow is the shape of a tile's trend: a triangle for up or down, a dash for
// flat. Lower is better, so up is red and down is green; the words beside it say
// the same.
export function TrendArrow({ direction }: { direction: TrendDirection }) {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" aria-hidden="true" className="shrink-0">
      {direction === "up" && <polygon points="6,0.5 0,11.5 12,11.5" className={ARROW_FILL.up} />}
      {direction === "down" && <polygon points="6,11.5 0,0.5 12,0.5" className={ARROW_FILL.down} />}
      {direction === "flat" && (
        <rect x="0" y="4.5" width="12" height="3" className={ARROW_FILL.flat} />
      )}
    </svg>
  );
}
