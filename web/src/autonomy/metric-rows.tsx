import type { CSSProperties } from "react";
import { captionClass } from "../caption";
import {
  ROW_CAP,
  SPARSE_W,
  STAGE_COUNT_X,
  STAGE_GAP,
  STAGE_NAME_COL,
  STAGE_RATE_COL,
  WIDE_STAGE,
  padxFor,
} from "./axis";
import {
  NO_RATE,
  SPLIT_KINDS,
  UNKNOWN_MODEL,
  noAgentNote,
  phaseWord,
  prsWord,
  servedRate,
  splitOf,
  sumOf,
  whole,
} from "./format";
import { Diamond, Segment, Swatch } from "./marks";
import type { SplitSegment } from "./marks";
import type { Group, ModelRow, Phase } from "./types";

// A row of the by-repository chart: what the group is called, and its figures.
export type RowSpec = {
  key: string;
  name: string;
  group: Group;
};

const AXIS = [0, 0.2, 0.4, 0.6, 0.8];
// The scale a bar column too narrow for five labels carries.
const AXIS_SPARSE = [0, 0.4, 0.8];
const BAR_H = 14;
// The bar of a compact row, which its rate line sits beside.
const COMPACT_BAR_H = 12;
// The bar of a row that folds several models, thin so it reads as no one model's.
export const FOLD_BAR_H = 6;
// The value column of a phone row, and the gap between it and its bar: the scale
// above the bars stops short of both so its labels sit over the bars.
const VALUE_COL = "7.4rem";
const VALUE_GAP = "0.75rem";
// The wide stage row's columns, and the name and count inside the first.
const STAGE_GRID: CSSProperties = {
  gridTemplateColumns: `${STAGE_NAME_COL}px minmax(0, 1fr) ${STAGE_RATE_COL}px`,
  columnGap: STAGE_GAP,
};
const STAGE_NAME_GRID: CSSProperties = { gridTemplateColumns: `${STAGE_COUNT_X}px minmax(0, 1fr)` };

function axisLeft(tick: number): string {
  return `${(tick / ROW_CAP) * 100}%`;
}

// AxisLabels is the 0 to 80% scale over a column of bars.
function AxisLabels({ ticks }: { ticks: number[] }) {
  return (
    <div aria-hidden="true" className="relative h-4 text-cap text-ink2">
      {ticks.map((tick, i) => (
        <span
          key={tick}
          className="absolute"
          style={{
            left: axisLeft(tick),
            transform:
              i === 0
                ? undefined
                : i === ticks.length - 1
                  ? "translateX(-100%)"
                  : "translateX(-50%)",
          }}
        >
          {whole(tick)}
        </span>
      ))}
    </div>
  );
}

type PooledBarProps = {
  group: Group;
  height: number;
  split: boolean;
  // A muted bar is not one model's rate: it sums several.
  tone?: "accent" | "muted";
};

// PooledBar is a group's pooled rate as a bar on the 0 to 80% axis, in segments
// where the owner lines are split three ways. A rate above the axis fills the
// bar; a rate of zero keeps a 2px mark so it is still a mark.
function PooledBar({ group, height, split, tone = "accent" }: PooledBarProps) {
  const rate = group.rate_value;
  const share = rate === null ? 0 : Math.min(rate / ROW_CAP, 1) * 100;
  const parts = split && group.owner_lines > 0;
  let left = 0;
  return (
    <svg
      className="block w-full"
      height={height}
      aria-hidden="true"
      data-mark="pooled-bar"
      data-rate={rate ?? undefined}
    >
      <rect width="100%" height={height} className="fill-track" />
      <g data-mark="pooled-fill">
        {rate !== null && !parts && (
          <rect
            height={height}
            className={tone === "muted" ? "fill-muted" : "fill-accent"}
            style={{ width: `max(2px, ${share}%)` }}
          />
        )}
        {rate !== null &&
          parts &&
          SPLIT_KINDS.map((kind) => {
            const width = (share * splitOf(group)[kind]) / group.owner_lines;
            const x = left;
            left += width;
            return width > 0 ? (
              <Segment key={kind} kind={kind} x={`${x}%`} width={`${width}%`} height={height} />
            ) : null;
          })}
      </g>
    </svg>
  );
}

// Whisker is the median PR's rate as a diamond with a line out to the p90 PR's.
function Whisker({ group }: { group: Group }) {
  if (group.median_value === null || group.p90_value === null) {
    return null;
  }
  const median = Math.min(group.median_value / ROW_CAP, 1) * 100;
  const p90 = Math.min(group.p90_value / ROW_CAP, 1) * 100;
  return (
    <svg
      className="block w-full overflow-visible"
      height={16}
      aria-hidden="true"
      data-mark="whisker"
    >
      <line
        x1={`${median}%`}
        x2={`${p90}%`}
        y1={8}
        y2={8}
        strokeWidth={1.5}
        className="stroke-ink"
      />
      <line x1={`${p90}%`} x2={`${p90}%`} y1={4} y2={12} strokeWidth={1.5} className="stroke-ink" />
      <Diamond cx={`${median}%`} cy={8} r={5} />
    </svg>
  );
}

function MedianBar({ group }: { group: Group }) {
  if (group.median_value === null) {
    return null;
  }
  return (
    <svg className="block w-full" height={8} aria-hidden="true" data-mark="median-bar">
      <rect width="100%" height={8} className="fill-track" />
      <rect
        height={8}
        className="fill-ink2"
        style={{ width: `max(2px, ${Math.min(group.median_value / ROW_CAP, 1) * 100}%)` }}
      />
    </svg>
  );
}

function OffScale() {
  return (
    <svg width="12" height="12" viewBox="0 0 12 12" aria-hidden="true" className="shrink-0">
      <polygon points="6,0.5 0,11.5 12,11.5" className="fill-ink" data-mark="off-scale" />
    </svg>
  );
}

// Split names each part of the owner lines, with its count and its swatch.
function SplitWords({ group, column }: { group: Group; column: boolean }) {
  const parts = splitOf(group);
  return (
    <ul
      className={`mt-2 text-cap text-ink2 ${column ? "flex flex-col gap-y-1" : "flex flex-wrap gap-x-3.5 gap-y-1"}`}
    >
      {SPLIT_KINDS.map((kind: SplitSegment) => (
        <li key={kind} className="flex items-center gap-1.5">
          <Swatch kind={kind} />
          {parts[kind].toLocaleString("en-US")} {kind}
        </li>
      ))}
    </ul>
  );
}

type RowProps = { row: RowSpec; split: boolean; wide: boolean };

function WideRow({ row, split }: Omit<RowProps, "wide">) {
  const { group } = row;
  const rate = group.rate_value;
  const off = rate !== null && rate > ROW_CAP;
  return (
    <li
      className="grid grid-cols-[minmax(0,4.1fr)_minmax(0,3.9fr)_minmax(0,2fr)] items-start gap-x-4 py-3"
      data-row={row.name}
    >
      <div>
        <p className="flex flex-wrap items-baseline gap-x-3">
          <span className="text-body font-semibold text-ink">{row.name}</span>
          <span className="text-label text-ink2">{prsWord(group.prs)}</span>
        </p>
        {split && group.owner_lines > 0 && <SplitWords group={group} column={false} />}
      </div>
      <div>
        {rate === null ? (
          <p className="text-cap text-ink2">no rate</p>
        ) : (
          <>
            <PooledBar group={group} height={BAR_H} split={split} />
            <div className="mt-3">
              <Whisker group={group} />
            </div>
          </>
        )}
      </div>
      <div>
        {rate !== null && (
          <>
            <p className="flex items-baseline gap-x-1.5">
              {off && <OffScale />}
              <span className="font-mono text-body font-semibold text-ink">
                {servedRate(group.rate, rate, "no rate")}
              </span>
              <span className="text-label text-ink2">{off ? "pooled, off scale" : "pooled"}</span>
            </p>
            {group.median !== null && group.p90 !== null && (
              <p className="mt-2 text-cap text-ink2">
                median {group.median}, p90 {group.p90}
              </p>
            )}
          </>
        )}
      </div>
    </li>
  );
}

function CompactRow({ row, split }: Omit<RowProps, "wide">) {
  const { group } = row;
  const rate = group.rate_value;
  const off = rate !== null && rate > ROW_CAP;
  return (
    <li className="py-3" data-row={row.name}>
      <p className="flex flex-wrap items-baseline justify-between gap-x-3">
        <span className="text-body font-semibold text-ink">{row.name}</span>
        <span className="text-cap text-ink2">{prsWord(group.prs)}</span>
      </p>
      {rate !== null ? (
        <div
          className="mt-2 grid items-center gap-y-2"
          style={{ gridTemplateColumns: `minmax(0, 1fr) ${VALUE_COL}`, columnGap: VALUE_GAP }}
        >
          <PooledBar group={group} height={COMPACT_BAR_H} split={split} />
          <p className="flex items-baseline gap-x-1.5">
            {off && <OffScale />}
            <span className="font-mono text-body font-semibold text-ink">
              {servedRate(group.rate, rate, "no rate")}
            </span>
            {!off && <span className="text-label text-ink2">pooled</span>}
          </p>
          {group.median_value !== null && (
            <>
              <MedianBar group={group} />
              <p className="flex items-baseline gap-x-1.5">
                <span className="font-mono text-body font-semibold text-ink">{group.median}</span>
                <span className="text-label text-ink2">median</span>
              </p>
            </>
          )}
        </div>
      ) : (
        <p className="mt-2 text-label text-ink2">no rate</p>
      )}
      {split && group.owner_lines > 0 && <SplitWords group={group} column />}
      {off && (
        <p className="mt-2 text-cap text-ink2">
          Off scale: the pooled bar stops at {whole(ROW_CAP)}.
        </p>
      )}
    </li>
  );
}

type MetricPanelProps = {
  // The panel's own caption, where the chart's header is not it.
  title?: string;
  rows: RowSpec[];
  split: boolean;
  wide: boolean;
  // Whether the bars are too narrow for the full scale.
  sparse: boolean;
  // Words for the rows the phone leaves out, when it leaves any.
  restNote: (rest: RowSpec[]) => string;
  padx: number;
};

// MetricPanel is one titled set of rows on a 0 to 80% axis. On a phone it keeps
// the groups that have a median, or the first when none does, and says in a line
// what it left to the Data table.
export function MetricPanel({
  title,
  rows,
  split,
  wide,
  sparse,
  restNote,
  padx,
}: MetricPanelProps) {
  const withSpread = rows.filter((row) => row.group.median_value !== null);
  const shown = wide ? rows : withSpread.length > 0 ? withSpread : rows.slice(0, 1);
  const rest = wide ? [] : rows.filter((row) => !shown.includes(row));
  const notes = shown.flatMap((row) => {
    const note = noAgentNote(row.name, row.group);
    return note === null ? [] : [note];
  });
  return (
    <div style={{ paddingInline: padx }}>
      <div
        className={
          wide
            ? "grid grid-cols-[minmax(0,4.1fr)_minmax(0,3.9fr)_minmax(0,2fr)] items-end gap-x-4"
            : ""
        }
      >
        {title === undefined ? <div /> : <h4 className={`${captionClass} text-ink2`}>{title}</h4>}
        {wide && <AxisLabels ticks={AXIS} />}
      </div>
      {!wide && (
        <div className="mt-2" style={{ marginRight: `calc(${VALUE_COL} + ${VALUE_GAP})` }}>
          <AxisLabels ticks={sparse ? AXIS_SPARSE : AXIS} />
        </div>
      )}
      <ul className="mt-1 divide-y divide-hair">
        {shown.map((row) =>
          wide ? (
            <WideRow key={row.key} row={row} split={split} />
          ) : (
            <CompactRow key={row.key} row={row} split={split} />
          ),
        )}
      </ul>
      {notes.map((note) => (
        <p key={note} className="mt-1 text-cap font-semibold text-ink2">
          {note}
        </p>
      ))}
      {rest.length > 0 && <p className="mt-2 text-cap text-ink2">{restNote(rest)}</p>}
    </div>
  );
}

// A row of a stage card: one model, or the smallest models folded into one row.
type StageRow = {
  key: string;
  name: string;
  group: Group;
  // Whether the row is the stage's main model, drawn in full detail.
  main: boolean;
  // Whether the row sums several models, drawn as one thin muted bar.
  folded: boolean;
};

// The most rows a stage card shows, the endpoint's unknown group's row included.
const STAGE_ROWS = 3;

// stageRows is a stage's rows: the main model first, the smallest models folded
// into one row where more would show than fit, and the endpoint's unknown group
// always last.
function stageRows(phase: Phase, models: ModelRow[]): StageRow[] {
  const unknown = models.filter((row) => row.model === UNKNOWN_MODEL);
  const known = models.filter((row) => row.model !== UNKNOWN_MODEL);
  // The unknown group keeps a row of its own, so the models have the rest, and a
  // fold needs a row too: two or more models over the rest fold into one.
  const slots = STAGE_ROWS - unknown.length;
  const shown = known.length <= slots ? known : known.slice(0, slots - 1);
  const folded = known.slice(shown.length);
  const rows: StageRow[] = shown.map((row, i) => ({
    key: row.model,
    name: phaseWord(phase, row.model),
    group: row,
    main: i === 0,
    folded: false,
  }));
  if (folded.length > 0) {
    rows.push({
      key: "other models",
      name: `${folded.length} other models`,
      group: sumOf(folded),
      main: false,
      folded: true,
    });
  }
  for (const row of unknown) {
    rows.push({
      key: row.model,
      name: phaseWord(phase, row.model),
      group: row,
      main: false,
      folded: false,
    });
  }
  return rows;
}

function spreadWords(group: Group): string {
  return group.median !== null && group.p90 !== null
    ? `median ${group.median}, p90 ${group.p90}`
    : "under ten PRs, so no median";
}

// WideStageRow is one row of a stage card across its columns: the name and PRs, the
// bar, and the rate. Only the main model's row carries the split, the whisker and
// the median and p90 in words; the others keep a thin bar and the rate in words.
function WideStageRow({ row }: { row: StageRow }) {
  const { group, main } = row;
  const rate = group.rate_value;
  const off = rate !== null && rate > ROW_CAP;
  return (
    <li className={main ? "py-3" : "py-2"} data-row={row.name}>
      <div className={`grid ${main ? "items-start" : "items-center"}`} style={STAGE_GRID}>
        <div>
          <div className="grid items-baseline" style={STAGE_NAME_GRID}>
            <span
              className={`${main ? "text-head font-semibold text-ink" : "text-label text-ink2"} min-w-0 pr-3 break-words`}
            >
              {row.name}
            </span>
            <span className="text-label text-ink2">{prsWord(group.prs)}</span>
          </div>
          {main && group.owner_lines > 0 && <SplitWords group={group} column={false} />}
        </div>
        <div className={main ? "mt-1" : undefined}>
          {rate === null ? (
            <p className="text-label text-ink2">{servedRate(group.rate, rate, NO_RATE)}</p>
          ) : (
            <>
              <PooledBar
                group={group}
                height={main ? BAR_H : FOLD_BAR_H}
                split={main}
                tone={row.folded ? "muted" : "accent"}
              />
              {main && group.median_value !== null && (
                <div className="mt-2">
                  <Whisker group={group} />
                </div>
              )}
            </>
          )}
        </div>
        <div>
          {rate !== null && (
            <>
              <p className="flex items-baseline gap-x-1.5">
                {off && <OffScale />}
                <span
                  className={`font-mono font-semibold ${main ? "text-body text-ink" : "text-label text-ink2"}`}
                >
                  {servedRate(group.rate, rate, NO_RATE)}
                </span>
                <span className="text-label text-ink2">{off ? "pooled, off scale" : "pooled"}</span>
              </p>
              {main && <p className="mt-2 text-cap text-ink2">{spreadWords(group)}</p>}
            </>
          )}
        </div>
      </div>
    </li>
  );
}

// StageRowItem is one row of a stage card on a narrow card: its name and PRs, its bar
// and its rate. Only the main model's row carries the whisker and the median and p90
// in words.
function StageRowItem({ row }: { row: StageRow }) {
  const { group } = row;
  const rate = group.rate_value;
  const off = rate !== null && rate > ROW_CAP;
  return (
    <li className="py-3" data-row={row.name}>
      <p className="flex flex-wrap items-baseline gap-x-3">
        <span
          className={`${row.main ? "text-head" : "text-body"} min-w-0 font-semibold break-words text-ink`}
        >
          {row.name}
        </span>
        <span className="text-label text-ink2">{prsWord(group.prs)}</span>
      </p>
      {rate === null ? (
        <p className="mt-1 text-label text-ink2">{servedRate(group.rate, rate, NO_RATE)}</p>
      ) : (
        <>
          <div
            className="mt-2 grid items-center"
            style={{ gridTemplateColumns: `minmax(0, 1fr) ${VALUE_COL}`, columnGap: VALUE_GAP }}
          >
            <PooledBar
              group={group}
              height={row.folded ? FOLD_BAR_H : COMPACT_BAR_H}
              split={!row.folded}
              tone={row.folded ? "muted" : "accent"}
            />
            <p className="flex items-baseline gap-x-1.5">
              {off && <OffScale />}
              <span className="font-mono text-body font-semibold text-ink">
                {servedRate(group.rate, rate, NO_RATE)}
              </span>
              <span className="text-label text-ink2">{off ? "pooled, off scale" : "pooled"}</span>
            </p>
          </div>
          {row.main && group.median !== null && (
            <div className="mt-1" style={{ marginRight: `calc(${VALUE_COL} + ${VALUE_GAP})` }}>
              <Whisker group={group} />
            </div>
          )}
          {row.main && <p className="mt-1 text-cap text-ink2">{spreadWords(group)}</p>}
        </>
      )}
    </li>
  );
}

type StageCardProps = {
  // The stage's place in a run, which its ring shows.
  numeral: number;
  // The stage itself, which names it and words the rows of a stage with no model.
  phase: Phase;
  // The PRs that went through the stage, of the measured ones.
  through: number;
  total: number;
  // The endpoint's rows for the stage, most PRs first.
  models: ModelRow[];
  width: number;
};

// StageCard is one stage's card: its numeral and name with how many of the
// measured PRs ran it, its own 0 to 80% axis, and its rows. From WIDE_STAGE the
// axis sits over the bar column on the title's line and the rows span their columns.
export function StageCard({ numeral, phase, through, total, models, width }: StageCardProps) {
  const padx = padxFor(width);
  const wide = width >= WIDE_STAGE;
  const rows = stageRows(phase, models);
  const notes = rows.flatMap((row) => {
    const note = noAgentNote(row.name, row.group);
    return note === null ? [] : [note];
  });
  return (
    <div style={{ paddingInline: padx }} data-stage={phase}>
      <div className={wide ? "grid items-center" : undefined} style={wide ? STAGE_GRID : undefined}>
        <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
          <span className="grid h-6 w-6 shrink-0 place-items-center rounded-full border border-ink2 font-mono text-cap font-semibold text-ink2">
            {numeral}
          </span>
          <h3 className={`${captionClass} text-ink`}>{phase}</h3>
          <p className="text-cap text-muted">
            {through === 0 ? "no model recorded" : `${through} of ${prsWord(total)}`}
          </p>
        </div>
        {wide && <AxisLabels ticks={AXIS} />}
      </div>
      {!wide && (
        <div className="mt-3" style={{ marginRight: `calc(${VALUE_COL} + ${VALUE_GAP})` }}>
          <AxisLabels ticks={width < SPARSE_W ? AXIS_SPARSE : AXIS} />
        </div>
      )}
      <ul className={`mt-1 ${wide ? "" : "divide-y divide-hair"}`}>
        {rows.map((row) =>
          wide ? (
            <WideStageRow key={row.key} row={row} />
          ) : (
            <StageRowItem key={row.key} row={row} />
          ),
        )}
      </ul>
      {notes.map((note) => (
        <p key={note} className="mt-1 text-cap font-semibold text-ink2">
          {note}
        </p>
      ))}
    </div>
  );
}
