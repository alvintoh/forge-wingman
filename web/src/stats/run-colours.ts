// One kind of settled run and the words the legend names it with.
type RunKind = { kind: string; words: string };

// One colour of the run strip and every kind drawn in it.
type RunColour = { fill: string; kinds: readonly RunKind[] };

// MUTED is the colour the muted kinds share, and the one a kind the table does
// not hold draws, so every bar takes a colour the legend shows.
const MUTED: RunColour = {
  fill: "bg-muted",
  kinds: [
    { kind: "cancelled", words: "cancelled" },
    { kind: "no-changes", words: "no changes" },
    { kind: "other", words: "other" },
  ],
};

// One table of the strip's colours, from the tokens the inbox already draws its
// reasons with: each colour and every kind drawn in it, so a bar and the legend
// read the same rows. The endpoint sends ten kinds, all of them here; a kind it
// adds later draws MUTED, which the legend shows though it does not name it.
export const RUN_COLOURS: readonly RunColour[] = [
  { fill: "bg-ok", kinds: [{ kind: "merged", words: "merged" }] },
  { fill: "bg-review", kinds: [{ kind: "pr-open", words: "PR open" }] },
  {
    fill: "bg-stop",
    kinds: [
      { kind: "failed", words: "failed" },
      { kind: "credential", words: "credential missing" },
      { kind: "decision", words: "unconfirmed decision" },
    ],
  },
  {
    fill: "bg-defer",
    kinds: [
      { kind: "allowance", words: "allowance spent" },
      { kind: "draft", words: "draft PR" },
    ],
  },
  MUTED,
];

// fillOf is the colour a kind of run is drawn in, read from the one table above;
// a kind it does not hold draws MUTED, as "other" does.
export function fillOf(kind: string): string {
  const colour = RUN_COLOURS.find((candidate) =>
    candidate.kinds.some((entry) => entry.kind === kind),
  );
  return (colour ?? MUTED).fill;
}
