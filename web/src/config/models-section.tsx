import { modelName } from "../autonomy/format";
import type { Read } from "../stats/use-read";
import { SwitchRow } from "./switch";
import { peakText } from "./types";
import type { ConfigView, PeakView } from "./types";

type ModelsSectionProps = {
  config: Read<ConfigView>;
  peak: Read<PeakView>;
  // Avoid peak hours as shown: the staged value, else the saved one; null until the mode is read.
  avoidPeak: boolean | null;
  disabled: boolean;
  onToggle: () => void;
};

function peakLine(view: ConfigView, model: string): string {
  const priced = view.peak_windows.find((entry) => entry.model === model);
  return priced === undefined ? "No peak window is known" : peakText(priced);
}

const PEAK_SENTENCE = {
  on: "Plan and build runs wait while their model's peak window is open, so they avoid the peak price.",
  off: "Runs start at any hour, including the peak window.",
} as const;

/**
 * ModelsSection lists the phases in the order they run, each with its model's full name and that
 * model's peak window, then the Avoid peak hours switch that acts on those windows.
 */
export function ModelsSection({ config, peak, avoidPeak, disabled, onToggle }: ModelsSectionProps) {
  return (
    <section
      aria-labelledby="config-models"
      aria-busy={config.status === "loading" || peak.status === "loading"}
      className="border-t border-hair pt-3.5 pb-5"
    >
      <h2
        id="config-models"
        className="flex min-h-11 items-center text-body font-semibold text-ink"
      >
        Models
      </h2>
      {/* A loading place holds the three rows: 168px while peak lines wrap, 132px from 451px. */}
      {config.status === "loading" && (
        <p role="status" className="min-h-42 text-label leading-5 text-ink2 min-[451px]:min-h-33">
          Reading the models…
        </p>
      )}
      {config.status === "error" && (
        <p role="alert" className="min-h-42 text-label leading-5 text-ink2 min-[451px]:min-h-33">
          The models could not be read.
        </p>
      )}
      {config.status === "ready" && (
        <dl className="space-y-3">
          {config.body.phases.map(({ phase, model }) => (
            <div
              key={phase}
              className="grid grid-cols-[auto_minmax(0,1fr)] items-baseline gap-x-4 text-label leading-4.5"
            >
              <dt className="text-ink2 capitalize">{phase}</dt>
              <dd className="text-right font-semibold wrap-break-word text-ink">
                {modelName(model)}
              </dd>
              <dd className="col-span-2 text-ink2">{peakLine(config.body, model)}</dd>
            </div>
          ))}
        </dl>
      )}
      <div className="mt-1">
        <SwitchRow
          name="Avoid peak hours"
          level="h3"
          on={avoidPeak}
          disabled={disabled}
          onToggle={onToggle}
        >
          {peak.status === "loading" && (
            <p role="status" className="min-h-10">
              Reading the peak mode…
            </p>
          )}
          {peak.status === "error" && (
            <p role="alert" className="min-h-10">
              The peak mode could not be read.
            </p>
          )}
          {avoidPeak !== null && (
            <p className="min-h-10">{avoidPeak ? PEAK_SENTENCE.on : PEAK_SENTENCE.off}</p>
          )}
        </SwitchRow>
      </div>
    </section>
  );
}
