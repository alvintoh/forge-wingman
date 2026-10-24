import { modelName } from "../autonomy/format";
import type { Read } from "../stats/use-read";
import { SwitchRow } from "./switch";
import { peakText } from "./types";
import type { ConfigView, PeakModel, PeakView } from "./types";

type ModelsSectionProps = {
  config: Read<ConfigView>;
  peak: Read<PeakView>;
  // Avoid peak hours as shown: the staged value, else the saved one; null until the mode is read.
  avoidPeak: boolean | null;
  disabled: boolean;
  onToggle: () => void;
};

type ModelLine = {
  model: string;
  phases: string;
  window: PeakModel | undefined;
};

// Each model once, in the order its first phase runs, with every phase it serves.
function modelLines(view: ConfigView): ModelLine[] {
  const phases = new Map<string, string[]>();
  for (const { phase, model } of view.phases) {
    phases.set(model, [...(phases.get(model) ?? []), phase]);
  }
  return [...phases].map(([model, served]) => ({
    model,
    phases: served.join(", "),
    window: view.peak_windows.find((priced) => priced.model === model),
  }));
}

const PEAK_SENTENCE = {
  on: "Plan and build runs wait while their model's peak window is open, so they avoid the peak price.",
  off: "Runs start at any hour, including the peak window.",
} as const;

/**
 * ModelsSection lists each model by its full name with the phases it serves and its peak window,
 * then the Avoid peak hours switch that acts on those windows.
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
      {/* While the models load their place holds two models' lines. */}
      {config.status === "loading" && (
        <p role="status" className="min-h-21 text-label leading-5 text-ink2">
          Reading the models…
        </p>
      )}
      {config.status === "error" && (
        <p role="alert" className="min-h-21 text-label leading-5 text-ink2">
          The models could not be read.
        </p>
      )}
      {config.status === "ready" && (
        <ul className="space-y-2">
          {modelLines(config.body).map((line) => (
            <li key={line.model} className="text-label leading-4.5">
              <div className="flex items-baseline justify-between gap-4">
                <p className="min-w-0 font-semibold wrap-break-word text-ink">
                  {modelName(line.model)}
                </p>
                <p className="shrink-0 text-ink2">{line.phases}</p>
              </div>
              <p className="text-ink2">
                {line.window === undefined ? "No peak window is known" : peakText(line.window)}
              </p>
            </li>
          ))}
        </ul>
      )}
      <div className="mt-2">
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
