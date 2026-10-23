import { ChartCard, ChartHeader } from "./chart-card";
import { COMPACT_W, SIZE_CAP, TRIANGLE_R, Y_LABEL_W, padxFor, yOf } from "./axis";
import { percent, prsWord, rateOrWords, referenceLabel } from "./format";
import { Diamond, RefLine } from "./marks";
import type { SizeRow } from "./types";

const TICKS = [0, 0.1, 0.2, 0.3, 0.4];
const PLOT_H = 140;
const BAR_W = 56;
const SLOT = 160;
// A fourth column, for PRs of no size, takes a narrower slot beside the legend.
const SLOT_FOUR = 120;
// The width from which the legend sits right of the chart.
const LEGEND_RIGHT_W = 900;

type BySizeProps = { sizes: SizeRow[]; bar: number };

// BySize is the pooled rate of each size as a diamond, with the median PR's rate
// as a bar and the p90's as a whisker, on its own 0 to 40% axis. A size under ten
// PRs has no median or p90 and says so; a pooled rate above the axis is the
// trend's triangle. A size with no PRs is not drawn. The phone leaves this chart
// to the Data table, as the drawing does.
export function BySize({ sizes, bar }: BySizeProps) {
  const shown = sizes.filter((size) => size.prs > 0);
  if (shown.length === 0) {
    return null;
  }
  return (
    <ChartCard minWidth={COMPACT_W}>
      {(width) => {
        const padx = padxFor(width);
        const right = width >= LEGEND_RIGHT_W;
        const x0 = padx + Y_LABEL_W;
        const slot = Math.min(
          right && shown.length > 3 ? SLOT_FOUR : SLOT,
          (width - padx - x0) / shown.length,
        );
        const x1 = x0 + slot * shown.length;
        const svgWidth = right ? x1 + 16 : width;
        const words = slot >= SLOT;
        const top = 8;
        const base = top + PLOT_H;
        const y = (value: number) => yOf(value, top, PLOT_H, SIZE_CAP);
        const height = base + 64;
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
                  {shown.map((size, i) => {
                    const cx = x0 + slot * (i + 0.5);
                    const median = size.median_value;
                    const p90 = size.p90_value;
                    const pooled = size.rate_value;
                    const off = pooled !== null && pooled > SIZE_CAP;
                    return (
                      <g key={size.size} data-size={size.size}>
                        {median === null || p90 === null ? (
                          <text
                            x={cx}
                            y={
                              pooled !== null && !off
                                ? Math.min(base - 14, y(pooled) - 12)
                                : base - 14
                            }
                            textAnchor="middle"
                            className="fill-ink2 text-label"
                          >
                            few PRs
                          </text>
                        ) : (
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
                            />
                            <g
                              strokeWidth={1.5}
                              className="stroke-ink"
                              data-mark="whisker"
                              data-rate={p90}
                            >
                              <line x1={cx} x2={cx} y1={y(median)} y2={y(p90)} />
                              <line x1={cx - 8} x2={cx + 8} y1={y(p90)} y2={y(p90)} />
                            </g>
                            <text
                              x={cx + BAR_W / 2 + 8}
                              y={y(median) + 14}
                              className="fill-ink text-label font-semibold"
                            >
                              {words ? `median ${percent(median)}` : percent(median)}
                            </text>
                            <text
                              x={cx + 12}
                              y={y(p90) + 4}
                              className="fill-ink text-label font-semibold"
                            >
                              {words ? `p90 ${percent(p90)}` : percent(p90)}
                            </text>
                          </>
                        )}
                        {pooled !== null && !off && (
                          <g data-mark="pooled" data-rate={pooled}>
                            <Diamond cx={cx} cy={y(pooled)} r={6} />
                          </g>
                        )}
                        {pooled !== null && off && (
                          <>
                            <polygon
                              points={`${cx},${top + 9 - TRIANGLE_R} ${cx - TRIANGLE_R},${top + 9 + TRIANGLE_R - 1} ${cx + TRIANGLE_R},${top + 9 + TRIANGLE_R - 1}`}
                              className="fill-ink"
                              data-mark="pooled"
                              data-rate={pooled}
                              data-clipped=""
                            />
                            <text
                              x={cx - 12}
                              y={top + 13}
                              textAnchor="end"
                              className="fill-ink text-label font-semibold"
                            >
                              {words
                                ? `pooled ${percent(pooled)}, off scale`
                                : `${percent(pooled)}, off scale`}
                            </text>
                          </>
                        )}
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
                          pooled {rateOrWords(pooled, "no rate")}
                        </text>
                      </g>
                    );
                  })}
                  <line x1={x0} x2={x1} y1={base} y2={base} className="stroke-ink2" />
                </g>
              </svg>
              <Legend bar={bar} right={right} padx={padx} />
            </div>
          </div>
        );
      }}
    </ChartCard>
  );
}

type LegendProps = { bar: number; right: boolean; padx: number };

function Legend({ bar, right, padx }: LegendProps) {
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
      <li className="mt-2 text-ink2">A size under ten PRs shows no median or p90.</li>
    </ul>
  );
}
