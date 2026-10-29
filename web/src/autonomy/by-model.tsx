import { padxFor } from "./axis";
import { ChartCard, ChartHeader } from "./chart-card";
import { SPLIT_KINDS, UNKNOWN_MODEL } from "./format";
import { Diamond, Swatch } from "./marks";
import { FOLD_BAR_H, StageCard } from "./metric-rows";
import { PHASES } from "./types";
import type { ModelRow, Phase } from "./types";

// The words under the three cards: what their figures are, and how they are read.
const LEGEND_NOTE =
  "Each bar is the model's pooled rate: the owner lines over the agent lines of the PRs it ran. Median and p90 are the spread of those PRs' own rates, served from ten PRs.";

type ByModelProps = { byModel: Record<Phase, ModelRow[]> };

// ByModel is each stage's models in a card of its own, numbered in the order a run
// takes them, and a slim card below holding the legend and the measured-figures note.
// Its label sits above the cards, level with the cards' content.
export function ByModel({ byModel }: ByModelProps) {
  return (
    <div>
      <div className="px-[7px] sm:px-[27px]">
        <ChartHeader title="by model" aside="bar = pooled rate" padx={0} />
      </div>
      <div className="mt-2.5 space-y-4 lg:space-y-6">
        {PHASES.map((phase, i) => {
          const rows = byModel[phase];
          if (rows.length === 0) return null;
          const total = rows.reduce((sum, row) => sum + row.prs, 0);
          const through = rows.reduce(
            (sum, row) => sum + (row.model === UNKNOWN_MODEL ? 0 : row.prs),
            0,
          );
          return (
            <ChartCard key={phase}>
              {(width) => (
                <StageCard
                  numeral={i + 1}
                  phase={phase}
                  through={through}
                  total={total}
                  models={rows}
                  width={width}
                />
              )}
            </ChartCard>
          );
        })}
        <ChartCard>{(width) => <LegendCard width={width} />}</ChartCard>
      </div>
    </div>
  );
}

// LegendCard is the slim card under the stages: the split's swatches, the marks the
// cards draw, and how the figures are measured.
function LegendCard({ width }: { width: number }) {
  return (
    <div style={{ paddingInline: padxFor(width) }}>
      <ul className="flex flex-wrap items-center gap-x-5 gap-y-1 text-cap text-ink">
        {SPLIT_KINDS.map((kind) => (
          <li key={kind} className="flex items-center gap-1.5">
            <Swatch kind={kind} />
            {kind}
          </li>
        ))}
        <li className="flex items-center gap-1.5">
          <svg width="30" height="12" aria-hidden="true" className="overflow-visible">
            <line x1="6" x2="24" y1="6" y2="6" strokeWidth={1.5} className="stroke-ink" />
            <Diamond cx={6} cy={6} r={5} />
          </svg>
          median PR, line to p90 PR
        </li>
        <li className="flex items-center gap-1.5">
          <svg width="10" height={FOLD_BAR_H} aria-hidden="true">
            <rect width="10" height={FOLD_BAR_H} className="fill-muted" />
          </svg>
          other models, pooled
        </li>
      </ul>
      <p className="mt-2 text-cap text-ink2">{LEGEND_NOTE}</p>
    </div>
  );
}
