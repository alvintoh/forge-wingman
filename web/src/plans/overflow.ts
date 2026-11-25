/**
 * What a scrolling table's region measures: its scroll position, its own width, its sticky label
 * column and how many plan columns the table holds. The label is 11rem wide, which is how every
 * other length here is scaled: a plan column is at least 14.5rem and the cue's gutter 3rem, so the
 * three stay in step when the reader's root font size is not 16px.
 */
export type Measure = {
  scrollLeft: number;
  clientWidth: number;
  label: number;
  plans: number;
};

const LABEL_REM = 11;
const COLUMN_REM = 14.5;
const GUTTER_REM = 3;

// Fractional widths round differently in each read, so a plan within a pixel of the edge is whole.
const TOLERANCE = 1;

function pixelsPerRem(measure: Measure): number {
  return measure.label / LABEL_REM;
}

/**
 * scrolls is whether the plans, each at its least width, are wider than the region beside the
 * label, so a reader has something to scroll to.
 */
export function scrolls(measure: Measure): boolean {
  const { clientWidth, label, plans } = measure;
  return (
    plans > 0 &&
    label > 0 &&
    label + plans * COLUMN_REM * pixelsPerRem(measure) > clientWidth + TOLERANCE
  );
}

// What is left of the region for the plan columns once the label and the cue's gutter are taken.
function room(measure: Measure): number {
  return measure.clientWidth - measure.label - GUTTER_REM * pixelsPerRem(measure);
}

/**
 * columnWidth is the width each plan column takes once the table scrolls: as many whole columns as
 * hold their least width fill the room beside the label and the gutter exactly, so at rest none is
 * cut off and at the end of the scroll the last one is whole.
 */
export function columnWidth(measure: Measure): number {
  const least = COLUMN_REM * pixelsPerRem(measure);
  const whole = Math.max(1, Math.floor((room(measure) + TOLERANCE) / least));
  return Math.max(least, room(measure) / whole);
}

/**
 * tableWidth is the scrolling table's width: the label and every plan at columnWidth. The scroll
 * adds the gutter after it, so the last plan can rest clear of the cue.
 */
export function tableWidth(measure: Measure): number {
  return measure.label + measure.plans * columnWidth(measure);
}

/**
 * plansToScroll is how many plans lie past the edge of the room the plans have, whole or in part:
 * the ones a reader scrolls right to reach. It is 0 when every plan fits, and again once the table
 * is scrolled to its end, where the plans left behind are to the left and the cue points right.
 */
export function plansToScroll(measure: Measure): number {
  if (!scrolls(measure)) {
    return 0;
  }
  const shown = Math.floor((measure.scrollLeft + room(measure) + TOLERANCE) / columnWidth(measure));
  return measure.plans - shown;
}

/** cueWords is the cue's words for the plans left to scroll to. */
export function cueWords(hidden: number): string {
  return hidden === 1 ? "+1 plan" : `+${hidden} plans`;
}
