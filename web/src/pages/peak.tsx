import { useEffect, useState } from "react";
import { cell } from "./shared";

type PeakHour = {
  from: number;
  to: number;
};

type PeakWindow = {
  multiplier: number;
  // The weekday UTC hour ranges the multiplier applies in, each [from, to).
  hours: PeakHour[];
};

type PeakModel = {
  model: string;
  // Null for a model priced the same at every hour, and for one its plan does
  // not price at all.
  peak: PeakWindow | null;
  // Whether the plan prices the model at all, so the view tells a model priced
  // the same at every hour from one it never prices.
  priced: boolean;
};

// The peak endpoint's own shape; holding is whether a peak window is open now.
type PeakView = {
  mode: string;
  holding: boolean;
  models: PeakModel[];
};

type PeakState = {
  status: "loading" | "error" | "ready";
  view: PeakView;
};

// The two modes a run waits out its model's peak-price hours under, as the
// endpoint names them (internal/dispatcher/peak.go), so a rename there is a
// compile error here rather than a control that silently posts neither.
const PEAK_MODE = { offPeakOnly: "off-peak-only", always: "always" } as const;

const modeOptions = [
  { value: PEAK_MODE.offPeakOnly, label: "Off-peak only", note: "a run waits out the window" },
  { value: PEAK_MODE.always, label: "Always", note: "a run starts at any hour" },
];

// noPeakView is what the section holds before the view is read and after a read
// that failed; it renders in neither state.
const noPeakView: PeakView = { mode: PEAK_MODE.offPeakOnly, holding: false, models: [] };

// Peak shows which models price peak hours and when, whether a peak window is
// open now, and the mode that decides it. The control is offered only where a
// configured model prices peak hours: with none there is nothing to hold.
export function Peak() {
  const [state, setState] = useState<PeakState>({ status: "loading", view: noPeakView });
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState(false);

  useEffect(() => {
    const controller = new AbortController();

    async function read() {
      const response = await fetch("/api/peak", { signal: controller.signal });
      if (!response.ok) {
        throw new Error(`the peak endpoint answered ${response.status}`);
      }
      const body: unknown = await response.json();
      if (!isPeak(body)) {
        throw new Error("the peak endpoint answered no view");
      }
      if (controller.signal.aborted) {
        return;
      }
      setState({ status: "ready", view: body });
    }

    read().catch((err: unknown) => {
      if (controller.signal.aborted) {
        return;
      }
      console.error("peakNotRead", err);
      setState({ status: "error", view: noPeakView });
    });

    return () => controller.abort();
  }, []);

  const view = state.view;
  // A model's window is what the mode acts on, so the control is offered only
  // where one has any.
  const offered = view.models.some((model) => model.peak !== null);

  // setMode switches the mode and adopts what the endpoint answers, so the
  // control reads the mode the store now holds; a failed switch leaves it on
  // the one that stands.
  async function setMode(mode: string) {
    setSaving(true);
    setSaveError(false);
    try {
      const response = await fetch("/api/peak", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ mode }),
      });
      if (!response.ok) {
        throw new Error(`the peak endpoint answered ${response.status}`);
      }
      const body: unknown = await response.json();
      if (!isPeak(body)) {
        throw new Error("the peak endpoint answered no view");
      }
      setState({ status: "ready", view: body });
    } catch (err: unknown) {
      console.error("peakModeNotSet", err);
      setSaveError(true);
    } finally {
      setSaving(false);
    }
  }

  return (
    <section className="mx-auto max-w-desk px-padx pt-4 pb-band">
      <h1 className="text-title font-semibold text-ink">Peak hours</h1>
      <p className="mt-2 text-body text-ink2">
        A run waits out its model's peak-price hours while the mode is off-peak only.
      </p>
      {state.status === "loading" && (
        <p role="status" className="mt-6">
          Reading the peak mode…
        </p>
      )}
      {state.status === "error" && (
        <p role="alert" className="mt-6">
          The peak mode could not be read.
        </p>
      )}
      {state.status === "ready" && (
        <>
          {offered ? (
            <>
              <fieldset className="mt-6" disabled={saving}>
                <legend className="text-body text-ink2">Mode</legend>
                <div className="mt-2 flex flex-col">
                  {modeOptions.map((option) => (
                    <label
                      key={option.value}
                      className="inline-flex min-h-11 cursor-pointer items-center gap-2 text-body"
                    >
                      <input
                        type="radio"
                        name="peak-mode"
                        value={option.value}
                        checked={view.mode === option.value}
                        onChange={() => setMode(option.value)}
                      />
                      <span className="font-semibold text-ink">{option.label}</span>
                      <span className="text-ink2">{option.note}</span>
                    </label>
                  ))}
                </div>
              </fieldset>
              <p role="status" className="mt-2 text-body text-ink2">
                {view.holding
                  ? "A peak window is open right now."
                  : "No peak window is open right now."}
              </p>
              {saveError && (
                <p role="alert" className="mt-2 text-body text-stop">
                  The mode could not be saved.
                </p>
              )}
            </>
          ) : (
            <p className="mt-6 text-body text-ink2">
              No configured model prices peak hours, so no run is held.
            </p>
          )}
          <h2 className="mt-8 text-head font-semibold text-ink">Models</h2>
          {view.models.length === 0 ? (
            <p className="mt-2 text-body text-ink2">No model is configured.</p>
          ) : (
            <div className="mt-4 overflow-x-auto">
              <table className="w-full text-body max-md:block">
                <caption className="sr-only">
                  Each model a run may use and the hours it prices peak
                </caption>
                <thead className="max-md:sr-only">
                  <tr className="border-b border-hair text-left">
                    <th scope="col" className="py-2 pr-4">
                      Model
                    </th>
                    <th scope="col" className="py-2">
                      Peak pricing
                    </th>
                  </tr>
                </thead>
                <tbody className="max-md:block">
                  {view.models.map((model) => (
                    <tr key={model.model} className="border-b border-hair max-md:block max-md:py-2">
                      <th
                        scope="row"
                        className="py-2 pr-4 text-left font-mono max-md:block max-md:py-1"
                      >
                        {model.model}
                      </th>
                      <td className={cell} data-label="Peak pricing">
                        {peakText(model)}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </section>
  );
}

// isPeak checks the top-level fields the view reads have their declared types.
function isPeak(body: unknown): body is PeakView {
  if (typeof body !== "object" || body === null) {
    return false;
  }
  if (!("mode" in body && "holding" in body && "models" in body)) {
    return false;
  }
  return (
    typeof body.mode === "string" && typeof body.holding === "boolean" && Array.isArray(body.models)
  );
}

// peakText is a model's peak pricing as the view words it: the hours it covers
// on weekdays (UTC) and what they cost, the words for a model priced the same at
// every hour, or the words for one the plan prices nowhere. Hours are whole
// hours, printed as a clock reads them.
function peakText(model: PeakModel): string {
  if (model.peak === null) {
    return model.priced ? "Flat: the same price at every hour" : "Not priced by the plan";
  }
  const hours = model.peak.hours
    .map(({ from, to }) => [from, to].map((hour) => `${hour}:00`.padStart(5, "0")).join("–"))
    .join(", ");
  return `${hours} UTC on weekdays, ×${model.peak.multiplier} the price`;
}
