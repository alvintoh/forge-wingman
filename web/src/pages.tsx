import { Outlet, useParams } from "@tanstack/react-router";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { InboxProvider, InboxSection } from "./inbox";
import { AppShell } from "./menu";
import { ProjectsSection } from "./projects";

export function Root() {
  return (
    <div className="bg-ground text-ink">
      <InboxProvider>
        <AppShell>
          <Outlet />
        </AppShell>
      </InboxProvider>
    </div>
  );
}

// useTwoColumns asks the grid whether it lays out two columns, so the container query that decides
// it is the only place the width is written.
function useTwoColumns(grid: React.RefObject<HTMLDivElement | null>) {
  const [twoColumns, setTwoColumns] = useState(false);
  useLayoutEffect(() => {
    const element = grid.current;
    if (element === null) {
      return;
    }
    const read = () =>
      setTwoColumns(getComputedStyle(element).gridTemplateColumns.trim().split(/\s+/).length > 1);
    read();
    const observer = new ResizeObserver(read);
    observer.observe(element);
    return () => observer.disconnect();
  }, [grid]);
  return twoColumns;
}

export function Inbox() {
  // Actions leads in the main column; stacked, Projects leads. The sections are rendered in the
  // order they are shown, so the Tab order matches what is on screen, and their keys keep each one's
  // state across the switch.
  const sections = [<InboxSection key="inbox" />, <ProjectsSection key="projects" />];
  const grid = useRef<HTMLDivElement>(null);
  const twoColumns = useTwoColumns(grid);
  return (
    // Two columns once the page's width holds the 368px Projects card, the 32px gap, the insets and
    // an Actions card at least as wide as Projects: 53rem. The right inset is the header's 26px
    // gutter, so the Projects card ends under the avatar; the left one is the same gutter, and 16px
    // more beside the menu column (lg), which takes the cards off the menu's edge.
    <div
      ref={grid}
      className="grid grid-cols-1 gap-y-[calc(var(--spacing-band)*2)] px-padx py-band @[53rem]:grid-cols-[minmax(0,1fr)_23rem] @[53rem]:gap-x-8 lg:@[53rem]:pl-[calc(var(--spacing-padx)+1rem)]"
    >
      {twoColumns ? sections : [...sections].reverse()}
    </div>
  );
}

export function Run() {
  const { runId } = useParams({ from: "/runs/$runId" });
  return <h1>Run {runId}</h1>;
}

type RunCache = {
  run_id: string;
  provider: string;
  model: string;
  // The phase's own cached share as the endpoint prints it: the share of its
  // first request and of its whole input, or the word that stands in for a
  // share the run recorded none of.
  build_cache: string;
  plan_cache: string;
};

// The comparison endpoint's own shape; this view lists the runs it reports.
type ComparisonResponse = {
  runs: RunCache[];
};

type RunsState = {
  status: "loading" | "error" | "ready";
  runs: RunCache[];
};

// Statistics lists the recent runs and the share of each phase's input the
// provider served from cache.
export function Stats() {
  const [state, setState] = useState<RunsState>({ status: "loading", runs: [] });

  useEffect(() => {
    const controller = new AbortController();

    async function read() {
      const response = await fetch("/api/comparison", { signal: controller.signal });
      if (!response.ok) {
        throw new Error(`the comparison endpoint answered ${response.status}`);
      }
      const body: unknown = await response.json();
      if (!isRuns(body)) {
        throw new Error("the comparison endpoint answered no run list");
      }
      if (controller.signal.aborted) {
        return;
      }
      setState({ status: "ready", runs: body.runs });
    }

    read().catch((err: unknown) => {
      if (controller.signal.aborted) {
        return;
      }
      console.error("comparisonNotRead", err);
      setState({ status: "error", runs: [] });
    });

    return () => controller.abort();
  }, []);

  return (
    <section className="mx-auto max-w-desk px-padx py-band">
      <h1 className="text-title font-semibold text-ink">Statistics</h1>
      <p className="mt-2 text-body text-ink2">
        Each run's cached share per phase, its first request then its whole phase.
      </p>
      {state.status === "loading" && (
        <p role="status" className="mt-6">
          Reading the runs…
        </p>
      )}
      {state.status === "error" && (
        <p role="alert" className="mt-6">
          The runs could not be read.
        </p>
      )}
      {state.status === "ready" && state.runs.length === 0 && (
        <p className="mt-6">No run is recorded.</p>
      )}
      {state.status === "ready" && state.runs.length > 0 && (
        <div className="mt-6 overflow-x-auto">
          <table className="w-full text-body">
            <caption className="sr-only">Recent runs and each phase's cached share</caption>
            <thead>
              <tr className="border-b border-hair text-left">
                <th scope="col" className="py-2 pr-4">
                  Run
                </th>
                <th scope="col" className="py-2 pr-4">
                  Provider
                </th>
                <th scope="col" className="py-2 pr-4">
                  Build cache
                </th>
                <th scope="col" className="py-2">
                  Plan cache
                </th>
              </tr>
            </thead>
            <tbody>
              {state.runs.map((run) => (
                <tr key={run.run_id} className="border-b border-hair">
                  <th scope="row" className="py-2 pr-4 text-left font-mono">
                    {run.run_id}
                  </th>
                  <td className="py-2 pr-4 font-mono">{run.provider}</td>
                  <td className="py-2 pr-4">{run.build_cache}</td>
                  <td className="py-2">{run.plan_cache}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

// isRuns narrows the endpoint's answer to the one thing this view can check
// about a document it did not build: that it carries a list of runs.
function isRuns(body: unknown): body is ComparisonResponse {
  return typeof body === "object" && body !== null && "runs" in body && Array.isArray(body.runs);
}

type WindowUsage = {
  name: string;
  limit_usd: number;
  used_usd: number;
};

type Plan = {
  provider: string;
  price_usd: number;
  verdict: string;
  window_usage: WindowUsage[] | "unknown";
};

// The providers endpoint's own shape; a plan whose provider reports no window
// carries the word "unknown" in place of its usage.
type ProvidersResponse = {
  plans: Plan[];
};

type PlansState = {
  status: "loading" | "error" | "ready";
  plans: Plan[];
};

export function Providers() {
  const [state, setState] = useState<PlansState>({ status: "loading", plans: [] });

  useEffect(() => {
    const controller = new AbortController();

    async function read() {
      const response = await fetch("/api/providers", { signal: controller.signal });
      if (!response.ok) {
        throw new Error(`the providers endpoint answered ${response.status}`);
      }
      const body: unknown = await response.json();
      if (!isPlans(body)) {
        throw new Error("the providers endpoint answered no plan list");
      }
      if (controller.signal.aborted) {
        return;
      }
      setState({ status: "ready", plans: body.plans });
    }

    read().catch((err: unknown) => {
      if (controller.signal.aborted) {
        return;
      }
      console.error("providersNotRead", err);
      setState({ status: "error", plans: [] });
    });

    return () => controller.abort();
  }, []);

  return (
    <section className="mx-auto max-w-desk px-padx py-band">
      <h1 className="text-title font-semibold text-ink">Providers</h1>
      <p className="mt-2 text-body text-ink2">
        Listed in the order a run walks them. Order and models are changed in configuration.
      </p>
      {state.status === "loading" && (
        <p role="status" className="mt-6">
          Reading the plans…
        </p>
      )}
      {state.status === "error" && (
        <p role="alert" className="mt-6">
          The plans could not be read.
        </p>
      )}
      {state.status === "ready" && state.plans.length === 0 && (
        <p className="mt-6">No plan is recorded.</p>
      )}
      {state.status === "ready" && state.plans.length > 0 && (
        <div className="mt-6 overflow-x-auto">
          <table className="w-full text-body max-md:block">
            <caption className="sr-only">Plans in the order a run walks them</caption>
            <thead className="max-md:sr-only">
              <tr className="border-b border-hair text-left">
                <th scope="col" className="py-2 pr-4">
                  Provider
                </th>
                <th scope="col" className="py-2 pr-4">
                  Price
                </th>
                <th scope="col" className="py-2 pr-4">
                  Window usage
                </th>
                <th scope="col" className="py-2">
                  Terms
                </th>
              </tr>
            </thead>
            <tbody className="max-md:block">
              {state.plans.map((plan) => (
                <tr key={plan.provider} className="border-b border-hair max-md:block max-md:py-2">
                  <th
                    scope="row"
                    className="py-2 pr-4 text-left font-mono max-md:block max-md:py-1"
                  >
                    {plan.provider}
                  </th>
                  <td className={cell} data-label="Price">
                    {usd(plan.price_usd)}
                  </td>
                  <td className={cell} data-label="Window usage">
                    <UsageList usage={plan.window_usage} />
                  </td>
                  <td className={cell} data-label="Terms">
                    {plan.verdict}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

// cell is a plans-table cell; below md its row stacks and its data-label heads it.
const cell =
  "py-2 pr-4 max-md:block max-md:py-1 max-md:pr-0 max-md:before:block max-md:before:text-label max-md:before:text-muted max-md:before:content-[attr(data-label)]";

function usd(amount: number): string {
  return `$${amount.toFixed(2)}`;
}

// isPlans narrows the endpoint's answer to the one thing this view can check
// about a document it did not build: that it carries a list of plans.
function isPlans(body: unknown): body is ProvidersResponse {
  return typeof body === "object" && body !== null && "plans" in body && Array.isArray(body.plans);
}

// UsageList lists a plan's windows, one bullet each; a provider that reports no
// window sent the word itself.
function UsageList({ usage }: { usage: WindowUsage[] | "unknown" }) {
  if (usage === "unknown") {
    return <>unknown</>;
  }
  return (
    <ul className="list-disc pl-4">
      {usage.map((window) => (
        <li key={window.name} className="min-[380px]:whitespace-nowrap">
          {window.name}: {usd(window.used_usd)} of {usd(window.limit_usd)}
        </li>
      ))}
    </ul>
  );
}

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
    <section className="mx-auto max-w-desk px-padx py-band">
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

// MergeState is the auto-merge state the endpoint serves: the owner's switch
// beside the rate's verdict over the trailing window, the bar it is compared to
// and the reason the pair gives.
type MergeState = {
  switch_on: boolean;
  rate_allows: boolean;
  rate_value: string;
  limit: string;
  // Window is how many trailing merged PRs the rate covers.
  window: number;
  effective: boolean;
  // Reason is why auto-merge is or is not effective: one of the endpoint's own
  // stable tokens, which reasonText words.
  reason: string;
};

// AutonomyView is what the autonomy page reads from the endpoint: the
// auto-merge state over the measured merges.
type AutonomyView = {
  auto_merge: MergeState;
};

type AutonomyState = {
  status: "loading" | "error" | "ready";
  view: AutonomyView;
};

// noView is what the section holds before the state is read and after a read
// that failed; it renders in neither state.
const noView: AutonomyView = {
  auto_merge: {
    switch_on: false,
    rate_allows: false,
    rate_value: "",
    limit: "",
    window: 0,
    effective: false,
    reason: "",
  },
};

// Autonomy shows whether an eligible pull request merges itself: the state over
// the trailing window, the rate behind it and the reason. The switch is the
// owner's setting, set with `runner auto-merge`.
export function Autonomy() {
  const [state, setState] = useState<AutonomyState>({ status: "loading", view: noView });

  useEffect(() => {
    const controller = new AbortController();

    async function read() {
      const response = await fetch("/api/autonomy", { signal: controller.signal });
      if (!response.ok) {
        throw new Error(`the autonomy endpoint answered ${response.status}`);
      }
      const body: unknown = await response.json();
      if (!isAutonomy(body)) {
        throw new Error("the autonomy endpoint answered no auto-merge state");
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
      console.error("autonomyNotRead", err);
      setState({ status: "error", view: noView });
    });

    return () => controller.abort();
  }, []);

  const merge = state.view.auto_merge;

  return (
    <section className="mx-auto max-w-desk px-padx py-band">
      <h1 className="text-title font-semibold text-ink">Autonomy</h1>
      <p className="mt-2 text-body text-ink2">Whether an eligible PR merges itself.</p>
      {state.status === "loading" && (
        <p role="status" className="mt-6">
          Reading the auto-merge state…
        </p>
      )}
      {state.status === "error" && (
        <p role="alert" className="mt-6">
          The auto-merge state could not be read.
        </p>
      )}
      {state.status === "ready" && (
        <>
          <p className="mt-6 text-title font-semibold text-ink">
            {merge.effective ? "Auto-merge is on" : "Auto-merge is off"}
          </p>
          <p className="mt-1 text-body text-ink2">
            {`Rate over the trailing ${merge.window} merged PRs: ${merge.rate_value} (bar ${merge.limit}).`}
          </p>
          <p className="mt-2 text-body text-ink2">{reasonText(merge.reason)}</p>
        </>
      )}
    </section>
  );
}

// isAutonomy narrows the endpoint's answer to the one thing this view can check
// about a document it did not build: that it carries an auto-merge state.
function isAutonomy(body: unknown): body is AutonomyView {
  return (
    typeof body === "object" &&
    body !== null &&
    "auto_merge" in body &&
    typeof body.auto_merge === "object" &&
    body.auto_merge !== null
  );
}

// reasonText is why auto-merge is on or off, as the view words the endpoint's
// own reason tokens.
function reasonText(reason: string): string {
  switch (reason) {
    case "on":
      return "The rate allows it and the switch is on.";
    case "switch-off":
      return "The owner's switch is off.";
    case "switch-unread":
      return "The switch could not be read, so auto-merge is off.";
    case "too-few-measured":
      return "Fewer merged PRs than the window holds are measured, or one carries an unmeasured commit.";
    case "no-agent-lines":
      return "The measured PRs carry no agent lines, so there is no rate.";
    case "rate-above-bar":
      return "The trailing rate is above the bar.";
    case "reverted":
      return "An auto-merged PR of the window was reverted.";
    case "evidence-unread":
      return "The measured merges could not be read.";
    default:
      return reason;
  }
}
