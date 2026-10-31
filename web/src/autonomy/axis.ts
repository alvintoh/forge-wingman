import { textWidth, utcDay, whole } from "./format";
import type { PR, Point } from "./types";

// The rate a trend and a strip stop at; a value above it is a triangle in a band.
export const CAP = 0.3;
// The axes of the by-size chart and of the model and repository bars.
export const SIZE_CAP = 0.4;
export const ROW_CAP = 0.8;

// How many merges one axis holds, newest kept.
const MAX_SLOTS = 25;

export const BAND_H = 36;

// The room above a plot's top line with no band over it, for its top label.
export const PLOT_TOP = 8;

// What the band above a chart says it holds.
export const BAND_LABEL = `off scale, above ${whole(CAP)}`;
export const TRIANGLE_R = 6;

// The width of the column the y labels sit in, left of the plot.
export const Y_LABEL_W = 34;

// prKey names a merged PR across repositories: its number is its own only within
// its repository, so two repositories' PRs of one number are two PRs.
export function prKey(repo: string, pr: number): string {
  return `${repo}#${pr}`;
}

// A slot is one merged PR on the merge axis.
type Slot = { repo: string; pr: number; at: number };

// mergeSlots is the one merge axis of the trend and the strip: every PR either
// draws, oldest to newest, the newest 25 kept. The trend draws a point only where
// the series has one and the strip a dot only where a PR is listed, so a dot is
// never off its PR's slot, at any history length.
export function mergeSlots(series: Point[], prs: PR[]): Slot[] {
  const slots = new Map<string, Slot>();
  for (const point of series) {
    slots.set(prKey(point.repo, point.pr), {
      repo: point.repo,
      pr: point.pr,
      at: Date.parse(point.merged_at),
    });
  }
  // The PRs are listed newest first; the sort is stable, so ties keep merge order.
  for (const pr of [...prs].reverse()) {
    const key = prKey(pr.repo, pr.pr);
    if (!slots.has(key)) {
      slots.set(key, { repo: pr.repo, pr: pr.pr, at: Date.parse(pr.merged_at) });
    }
  }
  return [...slots.values()].sort((a, b) => a.at - b.at).slice(-MAX_SLOTS);
}

type AxisOptions = {
  width: number;
  padx: number;
  // The room right of the plot for labels.
  gutter: number;
  // Empty slots at each end, so a dot stays off the axis labels.
  blank?: number;
};

// Axis maps a slot to its x: one object for the trend and the strip, so the two
// cannot disagree.
export type Axis = {
  slots: Slot[];
  // The left edge of the labels, the plot's left and right edges, the slot step.
  ix: number;
  x0: number;
  x1: number;
  step: number;
  // x of the n-th slot, or of the PR's own slot, undefined where it has none.
  xOfIndex: (index: number) => number;
  xOfPR: (repo: string, pr: number) => number | undefined;
};

export function buildAxis(slots: Slot[], options: AxisOptions): Axis {
  const blank = options.blank ?? 0;
  const ix = options.padx;
  const x0 = ix + Y_LABEL_W;
  const x1 = options.width - options.padx - options.gutter;
  const steps = Math.max(slots.length + 2 * blank - 1, 1);
  const step = (x1 - x0) / steps;
  const index = new Map(slots.map((slot, i) => [prKey(slot.repo, slot.pr), i]));
  const xOfIndex = (i: number) => x0 + (i + blank) * step;
  return {
    slots,
    ix,
    x0,
    x1,
    step,
    xOfIndex,
    xOfPR: (repo, pr) => {
      const i = index.get(prKey(repo, pr));
      return i === undefined ? undefined : xOfIndex(i);
    },
  };
}

export function yOf(value: number, top: number, height: number, cap: number): number {
  return top + height * (1 - value / cap);
}

// Radii is the dot radius range at a slot step: the drawing's 13 where the steps
// allow it, shrinking toward a floor so neighbours never touch.
export type Radii = { max: number; min: number };

const R_CEIL = 13;
const R_FLOOR = 2.5;

export function radiiFor(step: number): Radii {
  const max = Math.min(R_CEIL, Math.max(R_FLOOR, (step - 1) / 2));
  return { max, min: max < R_CEIL ? R_FLOOR : 3 };
}

// dotRadius makes a dot's AREA proportional to its agent lines on one scale.
export function dotRadius(lines: number, mostLines: number, radii: Radii): number {
  if (mostLines <= 0) {
    return radii.min;
  }
  return Math.max(radii.min, radii.max * Math.sqrt(lines / mostLines));
}

type Vertex = { x: number; y: number };

// clippedPaths draws the series as lines, cut where it crosses the cap and broken
// where a point has no rate, so no line runs through the off-scale band or a gap.
export function clippedPaths(
  points: ({ x: number; value: number } | null)[],
  yOfValue: (value: number) => number,
  top: number,
): Vertex[][] {
  const paths: Vertex[][] = [];
  let current: Vertex[] = [];
  for (let i = 0; i + 1 < points.length; i++) {
    const a = points[i];
    const b = points[i + 1];
    if (a == null || b == null || (a.value > CAP && b.value > CAP)) {
      continue;
    }
    const cross = (from: { x: number; value: number }, to: { x: number; value: number }) =>
      from.x + ((CAP - from.value) / (to.value - from.value)) * (to.x - from.x);
    const start: Vertex =
      a.value <= CAP ? { x: a.x, y: yOfValue(a.value) } : { x: cross(a, b), y: top };
    const end: Vertex =
      b.value <= CAP ? { x: b.x, y: yOfValue(b.value) } : { x: cross(a, b), y: top };
    const last = current[current.length - 1];
    if (
      last !== undefined &&
      Math.abs(last.x - start.x) < 1e-6 &&
      Math.abs(last.y - start.y) < 1e-6
    ) {
      current.push(end);
    } else {
      if (current.length > 0) {
        paths.push(current);
      }
      current = [start, end];
    }
  }
  if (current.length > 0) {
    paths.push(current);
  }
  return paths;
}

type DateGroup = { label: string; first: number; last: number };

// dateGroups joins consecutive slots of one UTC day.
export function dateGroups(slots: Slot[]): DateGroup[] {
  const groups: DateGroup[] = [];
  slots.forEach((slot, i) => {
    const label = utcDay(slot.at);
    const last = groups[groups.length - 1];
    if (last !== undefined && last.label === label) {
      last.last = i;
    } else {
      groups.push({ label, first: i, last: i });
    }
  });
  return groups;
}

type DateLabel = { x: number; text: string; merges: number; row: 0 | 1 };

const DATE_SIZE = 12;
const MERGES_SIZE = 11;

// placeDates puts each day's label under its slots: the date on a first row and a
// merge count on a second, the largest groups first, and a label that would touch
// another is dropped (the Data section lists every date). A merge count is one word
// ("2 merges"), and a lone day close to another takes the second row.
export function placeDates(groups: DateGroup[], axis: Axis): DateLabel[] {
  const rows: { x: number; width: number }[][] = [[], []];
  const free = (row: 0 | 1, x: number, width: number, gap: number) =>
    rows[row].every((placed) => Math.abs(x - placed.x) >= (width + placed.width) / 2 + gap);
  const placed: DateLabel[] = [];
  const bySize = [...groups].sort((a, b) => b.last - b.first - (a.last - a.first));
  for (const group of bySize) {
    const merges = group.last - group.first + 1;
    const x = (axis.xOfIndex(group.first) + axis.xOfIndex(group.last)) / 2;
    const dateWidth = textWidth(group.label, DATE_SIZE);
    const mergesText = `${merges} merges`;
    const mergesWidth = textWidth(mergesText, MERGES_SIZE);
    if (merges > 1) {
      if (free(0, x, dateWidth, 10) && free(1, x, mergesWidth, 6)) {
        rows[0].push({ x, width: dateWidth });
        rows[1].push({ x, width: mergesWidth });
        placed.push({ x, text: group.label, merges, row: 0 });
      }
    } else if (free(0, x, dateWidth, 10)) {
      rows[0].push({ x, width: dateWidth });
      placed.push({ x, text: group.label, merges, row: 0 });
    } else if (free(1, x, dateWidth, 6)) {
      rows[1].push({ x, width: dateWidth });
      placed.push({ x, text: group.label, merges, row: 1 });
    }
  }
  return placed.sort((a, b) => a.x - b.x);
}

// bandLabelSide is where the band's words sit so no triangle is drawn over them:
// at the left, else at the right, else nowhere (the note under the chart says it).
export function bandLabelSide(
  xs: number[],
  x0: number,
  x1: number,
  label = BAND_LABEL,
): "start" | "end" | null {
  const room = textWidth(label, 12) + 16;
  if (xs.every((x) => x - x0 - 8 > room)) {
    return "start";
  }
  return xs.every((x) => x1 - 8 - x > room) ? "end" : null;
}

// The card width under which a chart takes the phone's geometry: tighter sides,
// no gutter and no PR numbers or model lanes, as the drawing's phone card does.
export const COMPACT_W = 520;
// The width from which the trend keeps its reference label and its measured note
// in a gutter beside the plot; under it they sit beneath the plot.
export const SIDE_W = 940;
export function isCompact(width: number): boolean {
  return width < COMPACT_W;
}

const DESKTOP_PADX = 26;

// The side padding of a chart card: the phone's, and the desktop's design padding.
export function padxFor(width: number): number {
  return isCompact(width) ? 16 : DESKTOP_PADX;
}

// The width from which a row of the model and repository bars is drawn across
// three columns, and under which its scale carries three labels, not five.
export const WIDE_ROWS = 640;
export const SPARSE_W = 380;

// The columns of a stage card's wide row, from the drawing: the model's name and
// its PR count (the count starts STAGE_COUNT_X in), the bar, and the rate beside
// the spread.
export const STAGE_NAME_COL = 424;
export const STAGE_COUNT_X = 330;
export const STAGE_RATE_COL = 194;
export const STAGE_GAP = 16;
// The narrowest bar column that keeps five scale labels apart, as the phone's.
const STAGE_BAR_MIN = 160;
// The card width from which a stage row is drawn across its columns.
export const WIDE_STAGE =
  2 * DESKTOP_PADX + STAGE_NAME_COL + STAGE_RATE_COL + 2 * STAGE_GAP + STAGE_BAR_MIN;

// The ticks of the trend and the strip, on the 0 to 30% axis.
export const TREND_TICKS = [0, 0.1, 0.2, 0.3];
