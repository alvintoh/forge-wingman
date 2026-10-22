/** The fill, border and ink of the app's two button kinds, shared by ActionButton and SmallPill. */
export const kindClass = {
  primary: "border-accent bg-accent text-card",
  secondary: "border-ink2 bg-card text-ink",
} as const;

/** The width style of one set of small pills, from pillSetWidth. */
export type PillWidth = { readonly width: string };

/**
 * pillSetWidth gives every pill of a set the width of the set's longest label: its glyphs of 1ch,
 * the 1.2px tracking after each, and 12px a side. RESUME is 70.8px and RETRY MERGE 109.8px in IBM
 * Plex Mono.
 */
export function pillSetWidth(labels: readonly string[]): PillWidth {
  const glyphs = Math.max(...labels.map((label) => label.length));
  return { width: `calc(${glyphs}ch + ${(glyphs * 1.2).toFixed(1)}px + 24px)` };
}
