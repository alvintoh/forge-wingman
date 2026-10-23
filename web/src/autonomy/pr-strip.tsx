import { BAND_H, BAND_LABEL, CAP, PLOT_TOP, bandLabelSide, dotRadius, radiiFor, yOf } from "./axis";
import type { Axis, Radii } from "./axis";
import { ChartHeader } from "./chart-card";
import { UNKNOWN_MODEL, count, phaseWord, textWidth, whole, wrap } from "./format";
import { Dot, Triangle } from "./marks";
import type { Phase, PR } from "./types";

const PLOT_H = 100;
const LANE_H = 28;
const LANE_LINE = 13;
const PHASES: { phase: Phase; label: string; pick: (pr: PR) => string }[] = [
  { phase: "plan", label: "plan model", pick: (pr) => pr.plan_model },
  { phase: "build", label: "build", pick: (pr) => pr.build_model },
  { phase: "review", label: "review", pick: (pr) => pr.review_model },
];

type Placed = { pr: PR; x: number };

type Pill = {
  x: number;
  width: number;
  word: boolean;
  inside: string | null;
  below: string[];
  cx: number;
  // Which row of wrapped names beneath the pills this pill's name sits on.
  level: number;
};

type Lane = { label: string; labelX: number; pills: Pill[]; height: number; rowHeight: number };

// The rows of wrapped names a lane may stack when neighbours' names would touch.
const MAX_LEVELS = 3;

// layoutLane puts one phase's models under the strip: one pill per run of
// neighbouring PRs that share a model, the model's full name inside when it fits
// and wrapped beneath when it does not, on the first row where it touches no
// other name. It is null where the names cannot be drawn apart, and the Data
// table then carries the models.
function layoutLane(
  axis: Axis,
  placed: Placed[],
  index: Map<number, number>,
  label: string,
  phase: Phase,
  pick: (pr: PR) => string,
): Lane | null {
  const runs: { model: string; first: number; last: number }[] = [];
  for (const { pr } of placed) {
    const slot = index.get(pr.pr);
    const model = pick(pr);
    const last = runs[runs.length - 1];
    if (
      slot !== undefined &&
      last !== undefined &&
      last.model === model &&
      last.last === slot - 1
    ) {
      last.last = slot;
    } else if (slot !== undefined) {
      runs.push({ model, first: slot, last: slot });
    }
  }
  const firstSlot = runs[0]?.first;
  if (firstSlot === undefined) {
    return null;
  }
  const labelX = axis.xOfIndex(firstSlot) - axis.step / 2 - 10;
  if (labelX - textWidth(label, 12) <= axis.x0 + 6) {
    return null;
  }
  const rows: { c: number; w: number }[][] = Array.from({ length: MAX_LEVELS }, () => []);
  let most = 0;
  let levels = 0;
  const pills: Pill[] = [];
  for (const run of runs) {
    const x = axis.xOfIndex(run.first) - axis.step / 2 + 2;
    const width = axis.xOfIndex(run.last) + axis.step / 2 - 2 - x;
    const name = phaseWord(phase, run.model);
    const inside = textWidth(name, 12) + 10 <= width;
    const below = inside ? [] : wrap(name, 11, Math.max(width, 64));
    const cx = x + width / 2;
    let level = 0;
    if (below.length > 0) {
      const wide = Math.max(...below.map((line) => textWidth(line, 11)));
      level = rows.findIndex((row) => row.every((e) => Math.abs(e.c - cx) >= (e.w + wide) / 2 + 2));
      if (level < 0) {
        return null;
      }
      rows[level]?.push({ c: cx, w: wide });
      most = Math.max(most, below.length);
      levels = Math.max(levels, level + 1);
    }
    pills.push({
      x,
      width,
      word: run.model === UNKNOWN_MODEL,
      inside: inside ? name : null,
      below,
      cx,
      level,
    });
  }
  return {
    label,
    labelX,
    pills,
    rowHeight: LANE_LINE * most,
    height: LANE_H + (levels > 0 ? 3 + LANE_LINE * most * levels : 0),
  };
}

// keySamples are the three dot sizes the strip's key shows, up to its biggest.
function keySamples(most: number): number[] {
  return [Math.max(1, Math.round(most * 0.02)), Math.round(most * 0.5), most];
}

type PrStripProps = {
  axis: Axis;
  prs: PR[];
  width: number;
  padx: number;
  compact: boolean;
};

// PrStrip draws each PR's own rate as a dot on the trend's merge axis: its area is
// the PR's agent lines on one scale across the strip, its size letter and number
// sit under it, and a rate above 30% is a triangle in a labelled band. Under it,
// where the room allows, the models each PR ran as one pill per run of equal ones.
export function PrStrip({ axis, prs, width, padx, compact }: PrStripProps) {
  const placed: Placed[] = prs
    .flatMap((pr) => {
      const x = axis.xOfPR(pr.pr);
      return x === undefined ? [] : [{ pr, x }];
    })
    .sort((a, b) => a.x - b.x);
  const index = new Map(axis.slots.map((slot, i) => [slot.pr, i]));
  const radii: Radii = compact ? { max: 6.5, min: 2.5 } : radiiFor(axis.step);
  const rMax = radii.max;
  const mostLines = Math.max(0, ...prs.map((pr) => pr.agent_lines));
  const clipped = placed.filter(({ pr }) => pr.rate_value !== null && pr.rate_value > CAP);
  const band = clipped.length > 0 ? BAND_H : 0;
  const top = band > 0 ? BAND_H : PLOT_TOP;
  const base = top + PLOT_H;
  const y = (value: number) => yOf(value, top, PLOT_H, CAP);
  // A PR number is mono, so its width is exact: no margin for an estimate.
  const widest = Math.max(...prs.map((pr) => `#${pr.pr}`.length));
  const numbers = !compact && widest * 11 * 0.6 + 3 <= axis.step;
  const firstX = placed[0]?.x ?? axis.x1;
  const samples = keySamples(mostLines);
  const keyWidth = firstX - rMax - 8 - axis.x0 - 4;
  const keyLeft = keyWidth >= 3 * (textWidth(count(mostLines), 11, true) + 8);
  const labelSide = bandLabelSide(
    clipped.map(({ x }) => x),
    axis.x0,
    axis.x1,
  );
  // The key's three samples in a row, each as wide as its dot and its figure.
  const keyPitch = 2 * rMax + 6 + textWidth(count(mostLines), 11, true) + 18;
  let cy = base + rMax + (numbers ? 36 : 22) + 6;
  const keyRowY = cy;
  if (!keyLeft && placed.length > 0) {
    cy += 2 * rMax + 14;
  }
  const lanes = compact
    ? []
    : PHASES.map((p) => layoutLane(axis, placed, index, p.label, p.phase, p.pick));
  const drawLanes = lanes.length > 0 && lanes.every((lane) => lane !== null);
  const laneTops: number[] = [];
  if (drawLanes) {
    for (const lane of lanes) {
      laneTops.push(cy);
      cy += lane?.height ?? 0;
    }
  }
  const height = cy + 4;

  return (
    <div className="pb-3">
      <div className="mt-4">
        <ChartHeader title="each PR's own rate" aside="dot area = agent lines" padx={padx} />
      </div>
      <svg
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        role="img"
        aria-label={`Each of the last ${prs.length} merged PRs' own rate, the area of its dot its agent lines. The Data table lists every figure.`}
        className="mt-3"
        data-chart="strip"
      >
        <g aria-hidden="true">
          {band > 0 && (
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
          {[0, 0.1, 0.2, 0.3].map((tick) => (
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
            y1={top}
            y2={top}
            strokeDasharray="3 3"
            className="stroke-ink2"
          />
          {keyLeft && placed.length > 0 && (
            <g data-mark="key">
              <rect
                x={axis.x0 + 4}
                y={top + 14}
                width={keyWidth}
                height={72}
                className="fill-card"
              />
              {samples.map((lines, i) => (
                <g key={lines}>
                  <Dot
                    cx={axis.x0 + 4 + (keyWidth / 3) * (i + 0.5)}
                    cy={top + 38}
                    r={dotRadius(lines, mostLines, radii)}
                  />
                  <text
                    x={axis.x0 + 4 + (keyWidth / 3) * (i + 0.5)}
                    y={top + 70}
                    textAnchor="middle"
                    className="fill-ink2 font-mono text-micro font-semibold"
                  >
                    {count(lines)}
                  </text>
                </g>
              ))}
              <text x={axis.x0 + 8} y={top + 84} className="fill-ink2 text-micro font-semibold">
                agent lines
              </text>
            </g>
          )}
          {placed.map(({ pr, x }) => {
            if (pr.rate_value === null) {
              return null;
            }
            return pr.rate_value > CAP ? (
              <Triangle
                key={pr.pr}
                cx={x}
                cy={BAND_H / 2 + 1}
                className="fill-accent"
                data-mark="strip-dot"
                data-pr={pr.pr}
                data-rate={pr.rate_value}
                data-clipped=""
              />
            ) : (
              <Dot
                key={pr.pr}
                cx={x}
                cy={y(pr.rate_value)}
                r={dotRadius(pr.agent_lines, mostLines, radii)}
                data-mark="strip-dot"
                data-pr={pr.pr}
                data-rate={pr.rate_value}
              />
            );
          })}
          <line x1={axis.x0} x2={axis.x1} y1={base} y2={base} className="stroke-ink2" />
          {placed.map(({ pr, x }) => (
            <g key={pr.pr}>
              <line x1={x} x2={x} y1={base} y2={base + 4} className="stroke-ink2" />
              <text
                x={x}
                y={base + rMax + 14}
                textAnchor="middle"
                className="fill-ink font-mono text-label font-semibold"
              >
                {pr.size}
              </text>
              {numbers && (
                <text
                  x={x}
                  y={base + rMax + 29}
                  textAnchor="middle"
                  className="fill-ink2 font-mono text-micro"
                >
                  #{pr.pr}
                </text>
              )}
            </g>
          ))}
          {!keyLeft && placed.length > 0 && (
            <g data-mark="key">
              {samples.map((lines, i) => {
                const x = axis.x0 + keyPitch * i;
                return (
                  <g key={lines}>
                    <Dot cx={x + rMax} cy={keyRowY + rMax} r={dotRadius(lines, mostLines, radii)} />
                    <text
                      x={x + rMax * 2 + 6}
                      y={keyRowY + rMax + 4}
                      className="fill-ink2 font-mono text-micro font-semibold"
                    >
                      {count(lines)}
                    </text>
                  </g>
                );
              })}
            </g>
          )}
          {drawLanes &&
            lanes.map((lane, i) => {
              const laneTop = laneTops[i];
              if (lane === null || laneTop === undefined) {
                return null;
              }
              return (
                <g key={lane.label} data-lane={lane.label}>
                  <text
                    x={lane.labelX}
                    y={laneTop + 19}
                    textAnchor="end"
                    className="fill-ink2 text-cap font-semibold"
                  >
                    {lane.label}
                  </text>
                  {lane.pills.map((pill) => (
                    <g key={pill.x}>
                      <rect
                        x={pill.x}
                        y={laneTop + 4}
                        width={pill.width}
                        height={22}
                        rx={Math.min(pill.width, 22) / 2}
                        strokeDasharray={pill.word ? "3 2" : undefined}
                        className={pill.word ? "fill-card stroke-ink2" : "fill-track stroke-ink2"}
                      />
                      {pill.inside !== null && (
                        <text
                          x={pill.cx}
                          y={laneTop + 19}
                          textAnchor="middle"
                          className="fill-ink text-cap font-semibold"
                        >
                          {pill.inside}
                        </text>
                      )}
                      {pill.below.map((line, k) => (
                        <text
                          key={line}
                          x={pill.cx}
                          y={laneTop + 40 + lane.rowHeight * pill.level + LANE_LINE * k}
                          textAnchor="middle"
                          className="fill-ink2 text-micro font-semibold"
                        >
                          {line}
                        </text>
                      ))}
                    </g>
                  ))}
                </g>
              );
            })}
        </g>
      </svg>
      <p className="mt-2 text-cap text-ink2" style={{ paddingInline: padx }}>
        S, M or L is the size.
        {band > 0 && ` Triangles mark a rate above ${whole(CAP)}.`}
      </p>
    </div>
  );
}
