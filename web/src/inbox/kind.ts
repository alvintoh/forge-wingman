import { sameRepository } from "../project-search";
import { type InboxRun, isStopped } from "./run";

export type Kind = "FORK" | "TODO" | "REVIEW" | "STOPPED";

export type Wait = { words: string; blocks: boolean; kind: Kind };

export const DRAFT_WORDS = "still a draft";

// Why a PR waits on the owner. The first three also block the merge.
export const waits: Record<string, Wait> = {
  "unconfirmed-decision": { words: "unconfirmed decision", blocks: true, kind: "FORK" },
  draft: { words: DRAFT_WORDS, blocks: true, kind: "TODO" },
  "checks-not-green": { words: "checks not green", blocks: true, kind: "REVIEW" },
  "front-end": { words: "front-end change", blocks: false, kind: "REVIEW" },
  "size-l": { words: "size L", blocks: false, kind: "REVIEW" },
  sampled: { words: "sampled", blocks: false, kind: "REVIEW" },
  "auto-merge-off": { words: "auto-merge is off", blocks: false, kind: "REVIEW" },
};

/** The kind chips above the list, in the order they are drawn. */
export const KIND_CHIPS = ["MERGE PR", "REVIEW", "RETRY"] as const;

export type KindChip = (typeof KIND_CHIPS)[number];

const KIND_WORDS: Record<KindChip, string> = {
  "MERGE PR": "merge PR",
  REVIEW: "review",
  RETRY: "retry",
};

/** kindOf is the chip a row draws. A draft on GitHub is TODO whatever reason is stored. */
export function kindOf(run: InboxRun): Kind {
  if (isStopped(run)) {
    return "STOPPED";
  }
  if (run.draft === true) {
    return "TODO";
  }
  const wait: Wait | undefined = waits[run.wait_reason];
  return wait?.kind ?? "REVIEW";
}

// A chip is read from the run alone, not from the pill drawn now, so a row mid-merge keeps its place.
function carries(run: InboxRun, chip: KindChip): boolean {
  switch (chip) {
    case "MERGE PR":
      return !isStopped(run);
    case "REVIEW":
      return kindOf(run) === "REVIEW";
    case "RETRY":
      return isStopped(run);
  }
}

/** filterRuns keeps the runs of the chosen project that carry the chosen chip; null chooses all. */
export function filterRuns(
  runs: InboxRun[],
  project: string | null,
  chip: KindChip | null,
): InboxRun[] {
  return runs.filter(
    (run) =>
      (project === null || sameRepository(run.repo, project)) &&
      (chip === null || carries(run, chip)),
  );
}

/** selectionWords names the choices made, the project then the kind, for a heading or a line. */
export function selectionWords(project: string | null, chip: KindChip | null): string[] {
  const words: string[] = [];
  if (project !== null) {
    words.push(project);
  }
  if (chip !== null) {
    words.push(KIND_WORDS[chip]);
  }
  return words;
}

/** shownWords says what the list shows after a choice: how many of the inbox, and for what. */
export function shownWords(
  shown: number,
  total: number,
  project: string | null,
  chip: KindChip | null,
): string {
  const choices = selectionWords(project, chip);
  if (choices.length === 0) {
    return "Showing every action";
  }
  const actions = total === 1 ? "action" : "actions";
  return `Showing ${shown} of ${total} ${actions} for ${choices.join(", ")}`;
}

/** emptyWords is what the list says when the choices leave no action. */
export function emptyWords(project: string | null, chip: KindChip | null): string {
  return `No action for ${selectionWords(project, chip).join(", ")}`;
}
