import type { InboxRun } from "./run";

// The two focusable cells of a row: the row itself, which opens its sheet, and its pill (MERGE PR, or
// RETRY on a stopped run with no pull request).
export type Cell = "row" | "pill";

// The attributes a cell carries, which the list's keys read back to find where the focus is.
const RUN_ATTR = "data-run";
const CELL_ATTR = "data-cell";

export function cellAttrs(run: string, cell: Cell): Record<string, string> {
  return { [RUN_ATTR]: run, [CELL_ATTR]: cell };
}

// One opening of a row's dialog. Each is a new object, so the same row opened again is a change.
export type Opened = { run: InboxRun };

// Marks the dialogs the list opens, which hand the focus back to it when they close.
export const LIST_DIALOG_ATTR = "data-inbox-dialog";
export const listDialog = { [LIST_DIALOG_ATTR]: "" };

// Where focus sits in the list: a row's run, and which of its cells.
export type Position = { run: string; cell: Cell };

export function positionOf(target: EventTarget | null): Position | null {
  if (!(target instanceof HTMLElement)) {
    return null;
  }
  const run = target.getAttribute(RUN_ATTR);
  const cell = target.getAttribute(CELL_ATTR);
  if (run === null || (cell !== "row" && cell !== "pill")) {
    return null;
  }
  return { run, cell };
}

export function cellIn(list: HTMLElement | null, run: string, cell: Cell): HTMLElement | null {
  return (
    list?.querySelector<HTMLElement>(
      `[${RUN_ATTR}="${CSS.escape(run)}"][${CELL_ATTR}="${cell}"]`,
    ) ?? null
  );
}
