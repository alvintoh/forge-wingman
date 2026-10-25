import { ChartCard, ChartHeader } from "./chart-card";
import { COMPACT_W, SIZE_CAP, TRIANGLE_R, Y_LABEL_W, bandLabelSide, padxFor, yOf } from "./axis";
import { prsWord, referenceLabel, servedRate, textWidth, whole } from "./format";
import { Diamond, RefLine } from "./marks";
import type { SizeRow } from "./types";

const TICKS = [0, 0.1, 0.2, 0.3, 0.4];
const PLOT_H = 140;
const BAR_W = 56;
const SLOT = 160;
// A fourth column, for PRs of no size, takes a narrower slot beside the legend.
const SLOT_FOUR = 120;
// Below the legend-right width the plot takes the room it has, to this slot.
const SLOT_MAX = 300;
// The width from which the legend sits right of the chart.
const LEGEND_RIGHT_W = 900;
// The room above the plot for the band of figures past the axis: a row each.
const OFF_ROW = 18;
const OFF_PAD = 6;
const PLOT_TOP = 8;
const OFF_WORDS = ", off scale";
// Half the widest line centred under a column, and the margin a label keeps from the drawing's edge.
const CENTRED_HALF = 48;
const LABEL_EDGE = 4;

type Kind = "p90" | "median" | "pooled";

// One figure of a column past the 40% axis: drawn as a triangle in the band above
// the plot, with its value, its bar or mark clamped to the axis.
type Off = { kind: Kind; value: number; text: string };

type Column = {
  size: SizeRow;
  off: Off[];
};

function offOf(size: SizeRow): Off[] {
  const figures: [Kind, number | null, string | null][] = [
    ["p90", size.p90_value, size.p90],
    ["median", size.median_value, size.median],
    ["pooled", size.rate_value, size.rate],
  ];
  return figures.flatMap(([kind, value, text]) =>
    value !== null && text !== null && value > SIZE_CAP ? [{ kind, value, text }] : [],
  );
}

const LABEL_SIZE = 13;

function labelWidth(text: string): number {
  return textWidth(text, LABEL_SIZE) * 1.05;
}

// How far right of a column's centre its labels reach: the median's beside its
// bar and the p90's beside its whisker, when they are on the axis.
function reach(size: SizeRow, words: boolean): number {
  const median =
    size.median_value !== null && size.median !== null && size.median_value <= SIZE_CAP
      ? BAR_W / 2 + 8 + labelWidth(words ? `median ${size.median}` : size.median)
      : 0;
  const p90 =
    size.p90_value !== null && size.p90 !== null && size.p90_value <= SIZE_CAP
      ? 12 + labelWidth(words ? `p90 ${size.p90}` : size.p90)
      : 0;
  return Math.max(median, p90);
}

type Layout = { slot: number; words: boolean; svgWidth: number };

// layout sizes the slots from the room: the legend-right layout has a fixed slot
// and a drawing as wide as its labels need, the other takes the whole card up to
// a slot cap, leaving the last column's labels room inside it.
function layout(shown: SizeRow[], width: number, padx: number, right: boolean): Layout {
  const x0 = padx + Y_LABEL_W;
  const n = shown.length;
  const last = shown[n - 1];
  for (const words of [true, false]) {
    if (right) {
      const slot = n > 3 ? SLOT_FOUR : SLOT;
      if (words && slot < SLOT) {
        continue;
      }
      const need = Math.max(
        ...shown.map(
          (size, i) => x0 + slot * (i + 0.5) + Math.max(reach(size, words), CENTRED_HALF),
        ),
      );
      return { slot, words, svgWidth: Math.max(x0 + slot * n + 16, need + 8) };
    }
    // The plot stays inside the card's padding, and the last column's labels, which
    // reach right of its centre, inside the drawing.
    const reachLast = Math.max(last === undefined ? 0 : reach(last, words), CENTRED_HALF);
    const slot = Math.min(
      SLOT_MAX,
      (width - padx - x0) / n,
      (width - LABEL_EDGE - x0 - reachLast) / (n - 0.5),
    );
    if (words && slot < SLOT) {
      continue;
    }
    return { slot, words, svgWidth: width };
  }
  return { slot: SLOT, words: false, svgWidth: width };
}

type BySizeProps = { sizes: SizeRow[]; bar: number };

// BySize is the pooled rate of each size as a diamond, with the median PR's rate
// as a bar and the p90's as a whisker, on its own 0 to 40% axis. A size under ten
// PRs has no bar or whisker; its PR count is under the axis. A pooled rate, median
// or p90 above the axis is clamped to it and drawn as a triangle in a band over
// the plot, with its value; the Data table lists every figure. A size with no PRs
// is not drawn. The phone leaves this chart to the Data table, as the drawing does.
export function BySize({ sizes, bar }: BySizeProps) {
  const shown = sizes.filter((size) => size.prs > 0);
  if (shown.length === 0) {
    return null;
  }
  const columns: Column[] = shown.map((size) => ({ size, off: offOf(size) }));
  const rows = Math.max(...columns.map((column) => column.off.length));
  return (
    <ChartCard minWidth={COMPACT_W}>
      {(width) => {
        const padx = padxFor(width);
        const right = width >= LEGEND_RIGHT_W;
        const x0 = padx + Y_LABEL_W;
        const { slot, words, svgWidth } = layout(shown, width, padx, right);
        const x1 = x0 + slot * shown.length;
        const bandH = rows > 0 ? OFF_PAD + rows * OFF_ROW + OFF_PAD : 0;
        const top = bandH > 0 ? bandH : PLOT_TOP;
        const base = top + PLOT_H;
        const y = (value: number) => yOf(Math.min(value, SIZE_CAP), top, PLOT_H, SIZE_CAP);
        const height = base + 64;
        const itemText = (off: Off) => {
          const label = words ? `${off.kind} ${off.text}` : off.text;
          const full = `${label}${OFF_WORDS}`;
          return 18 + labelWidth(full) <= slot - 8 ? full : label;
        };
        const bandSide = bandLabelSide(
          columns.flatMap((column, i) => (column.off.length > 0 ? [x0 + slot * (i + 0.5)] : [])),
          x0,
          x1,
          `off scale, above ${whole(SIZE_CAP)}`,
        );
        return (
          <div>
            <ChartHeader
              title="by size"
              aside="pooled rate, and the median and p90 of each PR's rate"
              padx={padx}
            />
            <div className={right ? "mt-3 flex items-start" : "mt-3"}>
              <svg
                width={svgWidth}
                height={height}
                viewBox={`0 0 ${svgWidth} ${height}`}
                role="img"
                aria-label="Pooled rate, median PR rate and p90 PR rate of each size. The Data table lists every figure."
                className="shrink-0"
                data-chart="by-size"
              >
                <g aria-hidden="true">
                  {bandH > 0 && (
                    <>
                      <rect x={x0} y={0} width={x1 - x0} height={bandH} className="fill-track" />
                      {bandSide !== null && (
                        <text
                          x={bandSide === "start" ? x0 + 8 : x1 - 8}
                          y={OFF_PAD + OFF_ROW / 2 + 4}
                          textAnchor={bandSide}
                          className="fill-ink2 text-cap"
                        >
                          off scale, above {whole(SIZE_CAP)}
                        </text>
                      )}
                    </>
                  )}
                  {TICKS.map((tick) => (
                    <g key={tick}>
                      <line x1={x0} x2={x1} y1={y(tick)} y2={y(tick)} className="stroke-hair" />
                      <text
                        x={x0 - 8}
                        y={y(tick) + 4}
                        textAnchor="end"
                        className="fill-ink2 text-cap"
                      >
                        {Math.round(tick * 100)}%
                      </text>
                    </g>
                  ))}
                  <RefLine x1={x0} x2={x1} y={y(bar)} />
                  {columns.map(({ size, off }, i) => {
                    const cx = x0 + slot * (i + 0.5);
                    const median = size.median_value;
                    const p90 = size.p90_value;
                    const pooled = size.rate_value;
                    const pooledOn = pooled !== null && pooled <= SIZE_CAP;
                    const spread = median !== null && p90 !== null;
                    const medianOn = median !== null && median <= SIZE_CAP;
                    const p90On = p90 !== null && p90 <= SIZE_CAP;
                    // The p90's words sit above its tick where the median's bar is
                    // too near below it for the two to be read apart.
                    const p90Above = medianOn && p90On && y(median) - y(p90) < 18;
                    return (
                      <g key={size.size} data-size={size.size}>
                        {!spread && pooled === null && (
                          <text
                            x={cx}
                            y={base - 14}
                            textAnchor="middle"
                            className="fill-ink2 text-label"
                          >
                            no rate
                          </text>
                        )}
                        {spread && (
                          <>
                            <rect
                              x={cx - BAR_W / 2}
                              y={y(median)}
                              width={BAR_W}
                              height={base - y(median)}
                              rx={3}
                              className="fill-accent"
                              data-mark="median"
                              data-rate={median}
                              data-clipped={medianOn ? undefined : ""}
                            />
                            <g
                              strokeWidth={1.5}
                              className="stroke-ink"
                              data-mark="whisker"
                              data-rate={p90}
                              data-clipped={p90On ? undefined : ""}
                            >
                              <line x1={cx} x2={cx} y1={y(median)} y2={y(p90)} />
                              <line x1={cx - 8} x2={cx + 8} y1={y(p90)} y2={y(p90)} />
                            </g>
                            {medianOn && size.median !== null && (
                              <text
                                x={cx + BAR_W / 2 + 8}
                                y={y(median) + 14}
                                className="fill-ink text-label font-semibold"
                              >
                                {words ? `median ${size.median}` : size.median}
                              </text>
                            )}
                            {p90On && size.p90 !== null && (
                              <text
                                x={cx + 12}
                                y={p90Above ? y(p90) - 6 : y(p90) + 4}
                                className="fill-ink text-label font-semibold"
                              >
                                {words ? `p90 ${size.p90}` : size.p90}
                              </text>
                            )}
                          </>
                        )}
                        {pooledOn && (
                          <g data-mark="pooled" data-rate={pooled}>
                            <Diamond cx={cx} cy={y(pooled)} r={6} />
                          </g>
                        )}
                        {off.map((item, row) => {
                          const text = itemText(item);
                          const itemWidth = 18 + labelWidth(text);
                          const left = Math.min(
                            Math.max(cx - itemWidth / 2, x0 + 2),
                            svgWidth - LABEL_EDGE - itemWidth,
                          );
                          const rowY = OFF_PAD + row * OFF_ROW + OFF_ROW / 2;
                          return (
                            <g key={item.kind}>
                              <polygon
                                points={`${left + 6},${rowY - TRIANGLE_R} ${left},${rowY + TRIANGLE_R - 1} ${left + 12},${rowY + TRIANGLE_R - 1}`}
                                className="fill-ink"
                                data-mark={item.kind === "pooled" ? "pooled" : "off-scale"}
                                data-kind={item.kind}
                                data-rate={item.value}
                                data-clipped=""
                              />
                              <text
                                x={left + 18}
                                y={rowY + 4}
                                className="fill-ink text-label font-semibold"
                              >
                                {text}
                              </text>
                            </g>
                          );
                        })}
                        <text
                          x={cx}
                          y={base + 20}
                          textAnchor="middle"
                          className="fill-ink font-mono text-body font-semibold"
                        >
                          {size.size}
                        </text>
                        <text
                          x={cx}
                          y={base + 38}
                          textAnchor="middle"
                          className="fill-ink2 text-cap"
                        >
                          {prsWord(size.prs)}
                        </text>
                        <text
                          x={cx}
                          y={base + 54}
                          textAnchor="middle"
                          className="fill-ink2 text-cap"
                        >
                          pooled {servedRate(size.rate, pooled, "no rate")}
                        </text>
                      </g>
                    );
                  })}
                  <line x1={x0} x2={x1} y1={base} y2={base} className="stroke-ink2" />
                </g>
              </svg>
              <Legend bar={bar} right={right} padx={padx} off={rows > 0} />
            </div>
          </div>
        );
      }}
    </ChartCard>
  );
}

type LegendProps = { bar: number; right: boolean; padx: number; off: boolean };

function Legend({ bar, right, padx, off }: LegendProps) {
  return (
    <ul
      className={`flex flex-col gap-y-2 text-label text-ink ${right ? "mt-2 min-w-0 flex-1 pl-16" : "mt-1"}`}
      style={right ? { paddingRight: padx } : { paddingInline: padx }}
    >
      <li className="flex items-center gap-3">
        <svg width="22" height="14" aria-hidden="true" className="shrink-0">
          <rect width="22" height="14" rx="3" className="fill-accent" />
        </svg>
        bar: median PR rate
      </li>
      <li className="flex items-center gap-3">
        <svg width="22" height="18" aria-hidden="true" className="shrink-0">
          <g strokeWidth={1.5} className="stroke-ink">
            <line x1="11" x2="11" y1="2" y2="16" />
            <line x1="3" x2="19" y1="2" y2="2" />
          </g>
        </svg>
        whisker: p90 PR rate
      </li>
      <li className="flex items-center gap-3">
        <svg width="22" height="14" aria-hidden="true" className="shrink-0 overflow-visible">
          <Diamond cx={11} cy={7} r={6} />
        </svg>
        pooled rate of the size
      </li>
      <li className="flex items-center gap-3">
        <svg width="22" height="4" aria-hidden="true" className="shrink-0 overflow-visible">
          <RefLine x1={1} x2={21} y={2} />
        </svg>
        {referenceLabel(bar)}
      </li>
      {off && (
        <li className="flex items-center gap-3">
          <svg width="22" height="12" aria-hidden="true" className="shrink-0">
            <polygon points="11,0.5 5,11.5 17,11.5" className="fill-ink" />
          </svg>
          off scale: a figure above {whole(SIZE_CAP)}, drawn at the axis
        </li>
      )}
      <li className="mt-2 text-ink2">A size under ten PRs shows no median or p90.</li>
    </ul>
  );
}
