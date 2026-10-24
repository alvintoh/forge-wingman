import type { ReactNode } from "react";
import {
  BAND_H,
  BAND_LABEL,
  CAP,
  PLOT_TOP,
  SIDE_W,
  TREND_TICKS,
  isCompact,
  padxFor,
  prKey,
  bandLabelSide,
  clippedPaths,
  dateGroups,
  mergeSlots,
  placeDates,
  buildAxis,
  yOf,
} from "./axis";
import type { Axis } from "./axis";
import { ChartCard, ChartHeader } from "./chart-card";
import { referenceLabel, seriesLabel, whole } from "./format";
import { Dot, RefLine, Triangle } from "./marks";
import { PrStrip } from "./pr-strip";
import type { PR, Point } from "./types";

const GUTTER = 168;
// The gutter the strip keeps right of its plot for its size key, with no trend above it.
const KEY_GUTTER = 140;
const MEASURED_HEAD = "How this is measured";
const MEASURED =
  "The rewrite rate is the lines changed by anyone other than the runner after it opened a PR, over the lines the runner wrote, pooled over the last ten merged PRs. It measures human effort, not code quality.";
const NO_TREND = "The trend starts once ten PRs are measured.";

// The plot heights: the side layout's is tall enough for the measured note to
// clear the reference label in the gutter.
const PH_SIDE = 276;
const PH_UNDER = 200;
const PH_COMPACT = 170;

type TrendPlotProps = {
  axis: Axis;
  width: number;
  series: Point[];
  bar: number;
  height: number;
  // Whether the x axis is labelled with dates, or only says it is merge order.
  dates: boolean;
  // Whether the reference line's sample sits in the gutter right of the plot.
  sample: boolean;
  label: string;
};

type Box = { band: number; top: number; base: number; height: number };

function boxOf(plot: number, clipped: boolean, dates: boolean): Box {
  const top = clipped ? BAND_H : PLOT_TOP;
  return {
    band: clipped ? BAND_H : 0,
    top,
    base: top + plot,
    height: top + plot + 4 + (dates ? 44 : 30),
  };
}

// TrendPlot draws a series on the shared merge axis: its line, cut where it
// crosses the 30% cap with a triangle in a labelled band for each value above it,
// the reference line at the bar and the dates the slots fall on. A point with no
// rate is a gap.
export function TrendPlot(props: TrendPlotProps) {
  const { axis, width, series, bar, height, dates, sample, label } = props;
  const byPR = new Map(series.map((point) => [prKey(point.repo, point.pr), point]));
  const values = axis.slots.map((slot) => byPR.get(prKey(slot.repo, slot.pr))?.rate_value ?? null);
  const clippedIndexes = values.flatMap((v, i) => (v !== null && v > CAP ? [i] : []));
  const box = boxOf(height, clippedIndexes.length > 0, dates);
  const y = (value: number) => yOf(value, box.top, height, CAP);
  const dotR = axis.step > 14 ? 4 : 3.5;
  const labelSide = bandLabelSide(
    clippedIndexes.map((i) => axis.xOfIndex(i)),
    axis.x0,
    axis.x1,
  );
  const paths = clippedPaths(
    values.map((value, i) => (value === null ? null : { x: axis.xOfIndex(i), value })),
    y,
    box.top,
  );
  const labels = dates ? placeDates(dateGroups(axis.slots), axis) : [];
  const groups = dates ? dateGroups(axis.slots) : [];

  return (
    <svg
      width={width}
      height={box.height}
      viewBox={`0 0 ${width} ${box.height}`}
      role="img"
      aria-label={label}
      data-chart="trend"
    >
      <g aria-hidden="true">
        {clippedIndexes.length > 0 && (
          <>
            <rect
              x={axis.x0}
              y={0}
              width={axis.x1 - axis.x0}
              height={BAND_H}
              className="fill-track"
            />
            {labelSide !== null && (
              <text
                x={labelSide === "start" ? axis.x0 + 8 : axis.x1 - 8}
                y={BAND_H / 2 + 4}
                textAnchor={labelSide}
                className="fill-ink2 text-cap"
              >
                {BAND_LABEL}
              </text>
            )}
          </>
        )}
        {TREND_TICKS.map((tick) => (
          <g key={tick}>
            <line x1={axis.x0} x2={axis.x1} y1={y(tick)} y2={y(tick)} className="stroke-hair" />
            <text x={axis.x0 - 8} y={y(tick) + 4} textAnchor="end" className="fill-ink2 text-cap">
              {Math.round(tick * 100)}%
            </text>
          </g>
        ))}
        <line
          x1={axis.x0}
          x2={axis.x1}
          y1={box.top}
          y2={box.top}
          strokeDasharray="3 3"
          className="stroke-ink2"
        />
        <RefLine x1={axis.x0} x2={axis.x1} y={y(bar)} />
        {sample && <RefLine x1={axis.x1 + 12} x2={axis.x1 + 34} y={y(bar)} />}
        {paths.map((path) => (
          <path
            key={path[0]?.x ?? 0}
            d={`M${path.map((v) => `${v.x.toFixed(2)} ${v.y.toFixed(2)}`).join(" L")}`}
            fill="none"
            strokeWidth={2}
            strokeLinejoin="round"
            strokeLinecap="round"
            className="stroke-accent"
          />
        ))}
        {values.map((value, i) => {
          const slot = axis.slots[i];
          if (value === null || slot === undefined) {
            return null;
          }
          return value > CAP ? (
            <Triangle
              key={prKey(slot.repo, slot.pr)}
              cx={axis.xOfIndex(i)}
              cy={BAND_H / 2 + 1}
              className="fill-accent"
              data-mark="trend-dot"
              data-repo={slot.repo}
              data-pr={slot.pr}
              data-rate={value}
              data-clipped=""
            />
          ) : (
            <Dot
              key={prKey(slot.repo, slot.pr)}
              cx={axis.xOfIndex(i)}
              cy={y(value)}
              r={dotR}
              data-mark="trend-dot"
              data-repo={slot.repo}
              data-pr={slot.pr}
              data-rate={value}
            />
          );
        })}
        <line x1={axis.x0} x2={axis.x1} y1={box.base} y2={box.base} className="stroke-ink2" />
        {axis.slots.map((slot, i) => (
          <line
            key={prKey(slot.repo, slot.pr)}
            x1={axis.xOfIndex(i)}
            x2={axis.xOfIndex(i)}
            y1={box.base}
            y2={box.base + 4}
            className="stroke-ink2"
          />
        ))}
        {dates ? (
          <>
            {groups.map((group) => (
              <line
                key={group.first}
                x1={axis.xOfIndex(group.first) - 3}
                x2={axis.xOfIndex(group.last) + 3}
                y1={box.base + 9}
                y2={box.base + 9}
                className="stroke-ink2"
              />
            ))}
            {labels.map((date) => (
              <g key={date.x}>
                <text
                  x={date.x}
                  y={box.base + 4 + (date.row === 0 ? 22 : 36)}
                  textAnchor="middle"
                  className="fill-ink2 text-cap font-semibold"
                  data-mark="date"
                >
                  {date.text}
                </text>
                {date.merges > 1 && (
                  <text
                    x={date.x}
                    y={box.base + 4 + 36}
                    textAnchor="middle"
                    className="fill-ink2 text-micro font-semibold"
                  >
                    {date.merges} merges
                  </text>
                )}
              </g>
            ))}
          </>
        ) : (
          <text x={axis.x0} y={box.base + 4 + 20} className="fill-ink2 text-cap">
            merge order, one step per merged PR
          </text>
        )}
      </g>
    </svg>
  );
}

type TrendCardProps = {
  series: Point[];
  prs: PR[];
  bar: number;
  // The card's caption at the right of its title, and whatever sits at its top.
  aside: string;
  lead?: ReactNode;
};

// TrendCard is the trailing-ten trend with the per-PR strip under it on the same
// merge axis. Under ten measured PRs there is no series, so the trend is one
// line saying so and the strip stands on its own axis.
export function TrendCard({ series, prs, bar, aside, lead }: TrendCardProps) {
  return (
    <ChartCard>
      {(width) => {
        const padx = padxFor(width);
        const compact = isCompact(width);
        const side = width >= SIDE_W;
        const hasTrend = series.length > 0;
        const slots = mergeSlots(series, prs);
        const gutter = hasTrend ? (side ? GUTTER : 0) : KEY_GUTTER;
        const axis = buildAxis(slots, {
          width,
          padx,
          gutter,
          blank: hasTrend ? 0 : 1,
        });
        const height = side ? PH_SIDE : compact ? PH_COMPACT : PH_UNDER;
        const clipped = series.some((point) => point.rate_value !== null && point.rate_value > CAP);
        const refY = yOf(bar, clipped ? BAND_H : PLOT_TOP, height, CAP);
        return (
          <>
            {lead !== undefined && (
              <div className="pb-3" style={{ paddingInline: padx }}>
                {lead}
              </div>
            )}
            {lead !== undefined && (
              <hr className="mx-0 border-hair" style={{ marginInline: padx }} />
            )}
            <div className={lead === undefined ? "" : "pt-4"}>
              <ChartHeader
                title={
                  compact
                    ? "trailing 10, after each PR"
                    : "trailing 10 rewrite rate, after each merged PR"
                }
                aside={aside}
                padx={padx}
              />
            </div>
            {hasTrend ? (
              <>
                <div className="relative mt-3">
                  <TrendPlot
                    axis={axis}
                    width={width}
                    series={series}
                    bar={bar}
                    height={height}
                    dates
                    sample={side}
                    label={seriesLabel(series)}
                  />
                  {side && (
                    <>
                      <div
                        className="absolute"
                        style={{ left: axis.x1 + 12, top: 4, width: GUTTER - 18 }}
                      >
                        <MeasuredNote />
                      </div>
                      <p
                        className="absolute text-label text-ink"
                        style={{
                          left: axis.x1 + 42,
                          top: refY,
                          width: GUTTER - 12 - 30,
                          transform: "translateY(-50%)",
                        }}
                      >
                        {referenceLabel(bar)}
                      </p>
                    </>
                  )}
                </div>
                <div style={{ paddingInline: padx }}>
                  <p className="mt-2 text-cap text-ink2">
                    Axis capped at {whole(CAP)}. Triangles mark values above it.
                  </p>
                  {!side && (
                    <>
                      <ReferenceLegend bar={bar} />
                      <div className="mt-3">
                        <MeasuredNote />
                      </div>
                    </>
                  )}
                </div>
              </>
            ) : (
              <p className="mt-3 text-label text-muted" style={{ paddingInline: padx }}>
                {NO_TREND}
              </p>
            )}
            <hr className="mt-4 border-hair" style={{ marginInline: padx }} />
            <PrStrip
              axis={axis}
              prs={prs}
              width={width}
              padx={padx}
              compact={compact}
              alone={!hasTrend}
            />
          </>
        );
      }}
    </ChartCard>
  );
}

function MeasuredNote() {
  return (
    <>
      <p className="text-cap font-semibold text-ink">{MEASURED_HEAD}</p>
      <p className="mt-1 text-cap text-ink2">{MEASURED}</p>
    </>
  );
}

// ReferenceLegend names the reference line under a chart that has no gutter for it.
function ReferenceLegend({ bar }: { bar: number }) {
  return (
    <p className="mt-2 flex items-center gap-x-2.5 text-cap text-ink">
      <svg width="20" height="4" aria-hidden="true" className="shrink-0 overflow-visible">
        <RefLine x1={1} x2={19} y={2} />
      </svg>
      {referenceLabel(bar)}
    </p>
  );
}
