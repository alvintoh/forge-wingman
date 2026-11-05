import { type InboxRun, isStopped } from "./run";

// A run's stop detail is free text from the run: paragraphs split at blank lines, and a span in
// backticks is data, set in mono with the backticks dropped. It is only ever shown as text.

export type DetailSpan = { text: string; mono: boolean };

// paragraphsOf splits a detail at its blank lines, a line holding only spaces or tabs counting as one.
function paragraphsOf(detail: string | undefined): string[] {
  if (detail === undefined) {
    return [];
  }
  return detail
    .split(/\n[ \t]*\n/)
    .map((paragraph) => paragraph.trim())
    .filter((paragraph) => paragraph !== "");
}

// spansOf reads a paragraph's backtick spans as mono. An odd number of backticks pairs nothing for
// certain, so the paragraph is shown as written, its backticks kept.
export function spansOf(paragraph: string): DetailSpan[] {
  const parts = paragraph.split("`");
  if (parts.length % 2 === 0) {
    return [{ text: paragraph, mono: false }];
  }
  return parts
    .map((text, index) => ({ text, mono: index % 2 === 1 }))
    .filter((span) => span.text !== "");
}

// stopDetailOf is a stopped run's detail as paragraphs; a run that waits on a merge shows none.
export function stopDetailOf(run: InboxRun): string[] {
  return isStopped(run) ? paragraphsOf(run.stop_detail) : [];
}
