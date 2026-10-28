import { useEffect } from "react";
import { CAP } from "../autonomy/axis";
import {
  UNKNOWN_MODEL,
  isRed,
  phaseWord,
  prsWord,
  servedRate,
  spreadText,
  tooFewForSpread,
  whole,
} from "../autonomy/format";
import { isRewrite } from "../autonomy/guard";
import { PHASES } from "../autonomy/types";
import type { ModelRow, Phase } from "../autonomy/types";
import { captionClass } from "../caption";

type RowProps = { phase: Phase; row: ModelRow; bar: number };

// One model: its name, its median PR on the right, the pooled rate and PR count beneath, and a thin
// bar for the median on the 0 to 30% axis the charts share. A model with a rate and under ten PRs has
// no right-hand cell: the count beneath is the only place it prints.
function ModelLine({ phase, row, bar }: RowProps) {
  const above = isRed(row.median_value, bar);
  const tone = above ? "text-defer" : "text-ink";
  const counted = tooFewForSpread(row);
  return (
    <li className="mt-3">
      <div className="flex items-baseline justify-between gap-3">
        <p className="min-w-0 text-label leading-4 font-semibold wrap-break-word">
          {phaseWord(phase, row.model)}
        </p>
        {!counted && (
          <p
            className={`shrink-0 font-mono font-semibold ${row.median === null ? "text-label" : "text-title"} ${tone}`}
          >
            {spreadText(row.median, row)}
          </p>
        )}
      </div>
      <div className="mt-0.5 flex justify-between gap-3 text-micro leading-4">
        <p className="min-w-0 text-muted">
          {`pooled ${servedRate(row.rate, row.rate_value, "no rate")}, ${prsWord(row.prs)}`}
        </p>
        {row.median !== null && (
          <p className="shrink-0 font-semibold text-ink2">
            {above ? `median PR, above ${whole(bar)}` : "median PR"}
          </p>
        )}
      </div>
      {row.median_value !== null && (
        <div
          aria-hidden="true"
          className={`mt-1.5 h-1 min-w-0.5 rounded-tag ${above ? "bg-defer" : "bg-ink"}`}
          style={{ width: `${Math.min(row.median_value / CAP, 1) * 100}%` }}
        />
      )}
    </li>
  );
}

type PhasesProps = { byModel: Record<Phase, ModelRow[]>; bar: number };

// The space between the phases.
const phasesClass = "space-y-5";

function Phases({ byModel, bar }: PhasesProps) {
  return (
    <div className={`mt-[11px] ${phasesClass}`}>
      {PHASES.map((phase) => {
        const rows = byModel[phase];
        const recorded = rows.some((row) => row.model !== UNKNOWN_MODEL);
        return (
          <div key={phase}>
            <h3 className={`${captionClass} text-ink2`}>{phase}</h3>
            {recorded ? (
              <ul>
                {rows.map((row) => (
                  <ModelLine key={row.model} phase={phase} row={row} bar={bar} />
                ))}
              </ul>
            ) : (
              <p className="mt-3 text-label leading-4 font-semibold text-ink2">not recorded</p>
            )}
          </div>
        );
      })}
    </div>
  );
}

/** PhaseSlots holds each phase's heading and one model line's height while the figures load. */
export function PhaseSlots() {
  return (
    <div className={`mt-3.5 border-t border-hair pt-[12.5px] ${phasesClass}`}>
      {PHASES.map((phase) => (
        <div key={phase}>
          <h3 className={`${captionClass} text-ink2`}>{phase}</h3>
          <div className="mt-3 h-12.25" />
        </div>
      ))}
    </div>
  );
}

type PhaseModelsProps = { figures: unknown; bar: number };

/**
 * PhaseModels lists each phase's models with their median PR and pooled rate, from the endpoint's
 * rewrite figures. A median strictly above the bar is in the defer colour and says so in words.
 */
export function PhaseModels({ figures, bar }: PhaseModelsProps) {
  const readable = isRewrite(figures);
  useEffect(() => {
    if (!readable) {
      console.error(
        "phaseModelsNotRead",
        "the autonomy endpoint answered rewrite figures of another shape",
      );
    }
  }, [readable]);
  if (!isRewrite(figures)) {
    return (
      <p role="alert" className="mt-5 text-label text-ink2">
        The models could not be read.
      </p>
    );
  }
  return <Phases byModel={figures.by_model} bar={bar} />;
}
