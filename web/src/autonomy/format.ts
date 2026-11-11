import type { Group, MergeState, Phase, Point, PR, Tile } from "./types";

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
  "command-code/deepseek/deepseek-v4.1-flash": "DeepSeek V4.1 Flash (Command Code)",
  "command-code/meta/muse-spark-1.3-contributor": "Muse Spark 1.3 Contributor (Command Code)",
  "opencode-go/deepseek-v4.1-flash": "DeepSeek V4.1 Flash",
  "opencode-go/muse-spark-1.3-contributor": "Muse Spark 1.3 Contributor",
};

// evenTie is the fixed-point text of a value that sits exactly on a tie at the given decimal places
// and whose last kept digit is even, or null. There the endpoint's rounding keeps the digit, where
// JavaScript's rounds it up. A double's exact decimal expansion shows a tie as a 5 and then only
// zeros.
function evenTie(points: number, places: number): string | null {
  const [integer = "", fraction = ""] = points.toFixed(20).split(".");
  const kept = fraction.slice(0, places);
  const last = Number((integer + kept).slice(-1));
  if (!/^50*$/.test(fraction.slice(places)) || last % 2 !== 0) {
    return null;
  }
  return places > 0 ? `${integer}.${kept}` : integer;
}

// percent prints a fraction as the endpoint prints a rate, to one decimal, a tie
// to the even tenth: 0.0625 is "6.2%", as the endpoint's own text for it is. The
// page prints the endpoint's text wherever it has one; this words a figure the
// endpoint serves only as a number.
export function percent(fraction: number): string {
  const points = fraction * 100;
  return `${evenTie(points, 1) ?? points.toFixed(1)}%`;
}

// wholePercent prints a fraction as a whole percentage, a tie to the even
// point: 0.625 is "62%", as the endpoint's own text for a cache share is, and
// not the 63% JavaScript's rounding gives.
export function wholePercent(fraction: number): string {
  const points = fraction * 100;
  return `${evenTie(points, 0) ?? Math.round(points)}%`;
}

// whole prints a bar's fraction without a decimal it does not have: "10%".
export function whole(fraction: number): string {
  return `${Math.round(fraction * 1000) / 10}%`;
}

// barOf is the bar a rate is compared to, as a fraction, from the endpoint's own text for it:
// "20.0%" — the default bar — is 0.2.
export function barOf(limit: string): number {
  return Number.parseFloat(limit) / 100;
}

// servedRate is a rate as the endpoint prints it, or the page's words where the
// figure is null: a group of no PRs, or a window with no agent lines.
export function servedRate(text: string | null, value: number | null, words: string): string {
  return text === null || value === null ? words : text;
}

export const NO_RATE = "no rate";

// NO_AGENT_LINES is what the endpoint prints for a rate over none of the agent's
// lines.
const NO_AGENT_LINES = "no agent lines";

// tooFewForSpread is whether a group has a rate but no median, for want of PRs.
export function tooFewForSpread(group: Group): boolean {
  return group.median === null && group.agent_lines > 0;
}

// spreadText is a group's median or p90 as the endpoint prints it. Where it
// served none, the cell counts the group's PRs if it has a rate, else says it has none.
export function spreadText(text: string | null, group: Group): string {
  if (text !== null) {
    return text;
  }
  return tooFewForSpread(group) ? prsWord(group.prs) : NO_RATE;
}

export function count(value: number): string {
  return value.toLocaleString("en-US");
}

// prsWord counts PRs: "1 PR", "30 PRs".
export function prsWord(prs: number): string {
  return `${prs} PR${prs === 1 ? "" : "s"}`;
}

// isPrsWord is whether a text is a PR count as prsWord words it.
export function isPrsWord(text: string): boolean {
  return text === prsWord(Number.parseInt(text, 10));
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

// reasonText is what the endpoint's reason token says about the rate, or about
// the switch where the token is the switch's own. The rate gates auto-merge.
export function reasonText(reason: string): string {
  switch (reason) {
    case "on":
      return "The rewrite rate is at or under the bar.";
    case "switch-off":
      return "The owner's switch is off.";
    case "switch-unread":
      return "The auto-merge settings could not be read.";
    case "bar-invalid":
      return "The stored bar is not a whole number from 1 to 100.";
    case "escaped":
      return "An auto-merged PR escaped the owner's read.";
    case "too-few-measured":
      return "Too few PRs a human merged are measured, or one carries an unmeasured commit.";
    case "no-agent-lines":
      return "The measured PRs carry no agent lines, so there is no rate.";
    case "rate-above-bar":
      return "The trailing rate is above the bar.";
    case "evidence-unread":
      return "The measured merges could not be read.";
    default:
      return reason;
  }
}

/** rateMissing words why the rate cannot be computed, or is empty where the reason is not one of those. */
function rateMissing(merge: MergeState, reason: string): string {
  switch (reason) {
    case "too-few-measured":
      return `the rate cannot be computed: fewer than ${merge.window} PRs a human merged are measured`;
    case "no-agent-lines":
      return "the rate cannot be computed: the measured PRs carry no agent lines";
    case "evidence-unread":
      return "the rate cannot be computed: the measured merges could not be read";
    default:
      return "";
  }
}

/**
 * rateLine words what holds auto-merge: the trailing rate over the PRs a human merged and its place
 * against the bar, or the reason the rate cannot be used. The rate gates, so a rate above the bar,
 * exactly as much as a rate that cannot be computed, turns auto-merge off.
 */
export function rateLine(merge: MergeState): string {
  const trailing = `the trailing rewrite rate is ${merge.rate_value} over the last ${merge.window} PRs a human merged`;
  const bar = whole(barOf(merge.limit));
  switch (merge.reason) {
    case "switch-unread":
      return "off: the auto-merge settings could not be read";
    case "bar-invalid":
      return "off: the stored bar is not a whole number from 1 to 100";
    case "switch-off": {
      const missing = rateMissing(merge, merge.rate_reason);
      return missing
        ? `off: switched off by you; ${missing}`
        : `off: switched off by you; ${trailing}, ${isRed(barOf(merge.rate_value), barOf(merge.limit)) ? "above" : "at or under"} the ${bar} bar`;
    }
    case "escaped": {
      const how =
        merge.escaped_reason === "fixed"
          ? `was named as fixed by #${merge.escaped_by}`
          : "was reverted";
      return `off: ${merge.escaped_repo}#${merge.escaped_pr} ${how} within ${merge.escape_days} days of merging; auto-merge is off until you switch it on again`;
    }
    case "rate-above-bar":
      return `off: ${trailing}, above the ${bar} bar`;
    case "too-few-measured":
    case "no-agent-lines":
    case "evidence-unread":
      return `off: ${rateMissing(merge, merge.reason)}`;
    case "on":
      return `on: ${trailing}, at or under the ${bar} bar`;
    default:
      return reasonText(merge.reason);
  }
}

// isRed is whether a rate is strictly above the bar, which the page draws in red
// beside a triangle and the words.
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

// sumOf is several groups' figures added together: the PR, line and split counts
// summed, and the pooled rate over all of them. It carries no spread, which the
// endpoint reads off the PRs' own rates.
export function sumOf(groups: Group[]): Group {
  const total: Group = {
    prs: 0,
    rate: null,
    rate_value: null,
    median: null,
    median_value: null,
    p90: null,
    p90_value: null,
    agent_lines: 0,
    owner_lines: 0,
    review_found_lines: 0,
    owner_change_lines: 0,
    unlabelled_lines: 0,
    no_agent_lines_prs: 0,
    no_agent_owner_lines: 0,
  };
  for (const group of groups) {
    total.prs += group.prs;
    total.agent_lines += group.agent_lines;
    total.owner_lines += group.owner_lines;
    total.review_found_lines += group.review_found_lines;
    total.owner_change_lines += group.owner_change_lines;
    total.unlabelled_lines += group.unlabelled_lines;
    total.no_agent_lines_prs += group.no_agent_lines_prs;
    total.no_agent_owner_lines += group.no_agent_owner_lines;
  }
  const rate = total.agent_lines > 0 ? total.owner_lines / total.agent_lines : null;
  total.rate_value = rate;
  total.rate = rate === null ? NO_AGENT_LINES : percent(rate);
  return total;
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
  return `${whole(bar)}: the bar`;
}

// seriesLabel is the one sentence a screen reader gets for the trend; the Data
// table lists every figure.
export function seriesLabel(series: Point[]): string {
  const last = series[series.length - 1];
  const latest = last === undefined || last.rate_value === null ? "" : `, latest ${last.rate}`;
  return `Rolling rewrite rate after each of the last ${series.length} merged PRs${latest}. The Data section lists every figure.`;
}

// lastPrRepo names the repository of the last-PR tile's PR where the merged PRs span
// more than one, since its number alone does not say which. The newest PR leads the list.
export function lastPrRepo(prs: PR[], pr: number): string | null {
  const newest = prs[0];
  if (newest?.pr !== pr || new Set(prs.map((merged) => merged.repo)).size < 2) {
    return null;
  }
  return newest.repo;
}
