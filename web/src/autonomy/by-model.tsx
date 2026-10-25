import { ChartCard, ChartHeader } from "./chart-card";
import { SPARSE_W, WIDE_ROWS, padxFor } from "./axis";
import { SPLIT_KINDS, phaseWord, UNKNOWN_MODEL } from "./format";
import { Diamond, Swatch } from "./marks";
import { MetricPanel } from "./metric-rows";
import type { RowSpec } from "./metric-rows";
import { PHASES } from "./types";
import type { ModelRow, Phase } from "./types";

const PHASE_TITLE: Record<Phase, string> = { plan: "plan model", build: "build", review: "review" };

function specsOf(phase: Phase, rows: ModelRow[]): RowSpec[] {
  // A phase whose only group is the unknown one has no model recorded at all.
  const recorded = !(rows.length === 1 && rows[0]?.model === UNKNOWN_MODEL);
  return rows.map((group) => ({
    key: group.model,
    name: phaseWord(phase, group.model),
    group,
    recorded,
  }));
}

const restNote = () => "Other models have under ten PRs, so no median.";

type ByModelProps = { byModel: Record<Phase, ModelRow[]> };

// ByModel is each phase's models side by side with their pooled rate, the owner
// lines split into review-found, owner-change and unlabelled by pattern as well
// as by length, and each model's median PR with a line out to its p90.
export function ByModel({ byModel }: ByModelProps) {
  return (
    <ChartCard>
      {(width) => {
        const padx = padxFor(width);
        const wide = width >= WIDE_ROWS;
        const phases = PHASES.filter((phase) => byModel[phase].length > 0);
        return (
          <div>
            <ChartHeader title="by model" aside="bar = pooled rate" padx={padx} />
            {phases.map((phase, i) => (
              <div key={phase} className={i === 0 ? "mt-3" : "mt-3 border-t border-hair pt-3"}>
                <MetricPanel
                  title={PHASE_TITLE[phase]}
                  rows={specsOf(phase, byModel[phase])}
                  split
                  wide={wide}
                  sparse={width < SPARSE_W}
                  restNote={restNote}
                  padx={padx}
                />
              </div>
            ))}
            <ul
              className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-1 border-t border-hair pt-3 text-cap text-ink"
              style={{ marginInline: padx }}
            >
              {SPLIT_KINDS.map((kind) => (
                <li key={kind} className="flex items-center gap-1.5">
                  <Swatch kind={kind} />
                  {kind}
                </li>
              ))}
              {wide && (
                <li className="flex items-center gap-1.5">
                  <svg width="30" height="12" aria-hidden="true" className="overflow-visible">
                    <line x1="6" x2="24" y1="6" y2="6" strokeWidth={1.5} className="stroke-ink" />
                    <Diamond cx={6} cy={6} r={5} />
                  </svg>
                  median PR, line to p90 PR
                </li>
              )}
            </ul>
          </div>
        );
      }}
    </ChartCard>
  );
}
