import type { Group, Phase, Point, Tile } from "./types";

// The PR count a median and p90 need, as the endpoint gates them; the page words
// a smaller group "few PRs".
const SPREAD_MIN_PRS = 10;

// The model the endpoint names a phase that ran none, or more than one.
export const UNKNOWN_MODEL = "unknown";

// The words of a phase with no model, and the only phase that has a different
// word for it than its own.
const NO_MODEL_WORD: Record<Phase, string> = {
  plan: "no plan stage",
  build: "unknown (mixed or no model)",
  review: "no review",
};

// The names the page gives the models it knows; any other id reads as it is, so
// a new version never shows blank.
const MODEL_NAMES: Record<string, string> = {
  "command-code/deepseek/deepseek-v4.1-flash": "DeepSeek V4.1 Flash",
  "command-code/meta/muse-spark-1.3-contributor": "Muse Spark 1.3 Contributor",
};

// percent prints a fraction as the endpoint prints a rate: 0.122 is "12.2%".
export function percent(fraction: number): string {
  return `${(fraction * 100).toFixed(1)}%`;
}

// whole prints a bar's fraction without a decimal it does not have: "10%".
export function whole(fraction: number): string {
  return `${Math.round(fraction * 1000) / 10}%`;
}

// rateOrWords is a rate's figure, or the endpoint's own words for it when it has none.
export function rateOrWords(value: number | null, words: string): string {
  return value === null ? words : percent(value);
}

// spreadOrFew is a median or p90, or "few PRs" where the endpoint withheld it.
export function spreadOrFew(value: number | null): string {
  return value === null ? "few PRs" : percent(value);
}

export function count(value: number): string {
  return value.toLocaleString("en-US");
}

// prsWord counts PRs: "1 PR", "30 PRs".
export function prsWord(prs: number): string {
  return `${prs} PR${prs === 1 ? "" : "s"}`;
}

// countWords is a group's PR count, with the words that say it is too small for a spread.
export function countWords(prs: number): string {
  return prs < SPREAD_MIN_PRS ? `${prsWord(prs)}, few PRs` : prsWord(prs);
}

// utcDay is the UTC date of a timestamp, month then day: "10-17".
export function utcDay(timestamp: string | number): string {
  return new Date(timestamp).toISOString().slice(5, 10);
}

export function modelName(id: string): string {
  return MODEL_NAMES[id] ?? id;
}

// phaseWord is what a phase's group is called: the model, or the words for a phase that has none.
export function phaseWord(phase: Phase, model: string): string {
  return model === UNKNOWN_MODEL ? NO_MODEL_WORD[phase] : modelName(model);
}

// isRed is whether a rate is strictly above the bar, which the page draws in red
// beside a triangle and the words, and never to decide auto-merge.
export function isRed(value: number | null, bar: number): boolean {
  return value !== null && value > bar;
}

// tileWords is the words under a tile's number: how far a short window got, and
// for the trailing-ten tile its place against the bar. A tile never says more
// than the endpoint served.
export function tileWords(tile: Tile, index: number, bar: number): string | null {
  if (tile.prs.length < tile.asked) {
    return `${tile.prs.length} of ${tile.asked} PRs`;
  }
  if (index !== 0 || tile.rate_value === null) {
    return null;
  }
  return isRed(tile.rate_value, bar) ? `above ${whole(bar)}` : `at or below ${whole(bar)}`;
}

// trendWords is a tile's arrow in words, or null where the endpoint served no
// earlier window to compare it with.
export function trendWords(tile: Tile): string | null {
  if (tile.previous_rate_value === null) {
    return null;
  }
  if (tile.trend === "flat") {
    return "flat";
  }
  return `${tile.trend} from ${percent(tile.previous_rate_value)}`;
}

// The share of a group's lines the owner changed after review, as the three the
// endpoint splits them into.
export const SPLIT_KINDS = ["review-found", "owner-change", "unlabelled"] as const;
type SplitKind = (typeof SPLIT_KINDS)[number];

export function splitOf(group: Group): Record<SplitKind, number> {
  return {
    "review-found": group.review_found_lines,
    "owner-change": group.owner_change_lines,
    unlabelled: group.unlabelled_lines,
  };
}

// noAgentNote says a group's pooled rate carries PRs that had no agent lines.
export function noAgentNote(name: string, group: Group): string | null {
  if (group.no_agent_lines_prs === 0) {
    return null;
  }
  return `${name} pooled includes ${prsWord(group.no_agent_lines_prs)} with no agent lines: ${count(group.no_agent_owner_lines)} owner lines.`;
}

// The width a string takes at a size in the page's own faces, estimated from the
// average advance of IBM Plex Sans (0.522 em) and Mono (0.6 em) with the margin
// the drawing keeps for its error, so a label is fitted without measuring a node.
const SANS_ADVANCE = 0.522;
const MONO_ADVANCE = 0.6;
const ESTIMATE_MARGIN = 1.06;

export function textWidth(text: string, size: number, mono = false): number {
  return text.length * size * (mono ? MONO_ADVANCE : SANS_ADVANCE) * ESTIMATE_MARGIN;
}

// wrap breaks text into lines no wider than width, greedily by estimated width.
export function wrap(text: string, size: number, width: number): string[] {
  const lines: string[] = [];
  let line = "";
  for (const word of text.split(" ")) {
    const next = line === "" ? word : `${line} ${word}`;
    if (line !== "" && textWidth(next, size) > width) {
      lines.push(line);
      line = word;
    } else {
      line = next;
    }
  }
  lines.push(line);
  return lines;
}

// referenceLabel names the reference line at the bar.
export function referenceLabel(bar: number): string {
  return `${whole(bar)}: auto-merge on at or below, off above`;
}

// seriesLabel is the one sentence a screen reader gets for the trend; the Data
// table lists every figure.
export function seriesLabel(series: Point[]): string {
  const last = series[series.length - 1];
  const latest = last?.rate_value == null ? "" : `, latest ${percent(last.rate_value)}`;
  return `Rolling rewrite rate after each of the last ${series.length} merged PRs${latest}. The Data table lists every figure.`;
}
