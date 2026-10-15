import { Link, Outlet, useParams } from "@tanstack/react-router";
import { useEffect, useState } from "react";

export function Root() {
  return (
    <main className="min-h-dvh bg-ground text-ink">
      <Outlet />
    </main>
  );
}

export function Inbox() {
  return (
    <>
      <h1>Inbox</h1>
      <nav aria-label="Views" className="mt-2 flex flex-wrap gap-gap">
        <Link to="/autonomy" className="inline-flex min-h-11 min-w-11 items-center">
          Autonomy
        </Link>
        <Link to="/providers" className="inline-flex min-h-11 min-w-11 items-center">
          Providers
        </Link>
        <Link to="/stats" className="inline-flex min-h-11 min-w-11 items-center">
          Statistics
        </Link>
      </nav>
    </>
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
    <section className="mx-auto max-w-desk px-pad py-band">
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
    <section className="mx-auto max-w-desk px-pad py-band">
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
          <table className="w-full text-body">
            <caption className="sr-only">Plans in the order a run walks them</caption>
            <thead>
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
            <tbody>
              {state.plans.map((plan) => (
                <tr key={plan.provider} className="border-b border-hair">
                  <th scope="row" className="py-2 pr-4 text-left font-mono">
                    {plan.provider}
                  </th>
                  <td className="py-2 pr-4">{usd(plan.price_usd)}</td>
                  <td className="py-2 pr-4">{usageText(plan.window_usage)}</td>
                  <td className="py-2">{plan.verdict}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function usd(amount: number): string {
  return `$${amount.toFixed(2)}`;
}

// isPlans narrows the endpoint's answer to the one thing this view can check
// about a document it did not build: that it carries a list of plans.
function isPlans(body: unknown): body is ProvidersResponse {
  return typeof body === "object" && body !== null && "plans" in body && Array.isArray(body.plans);
}

// usageText renders a plan's window usage, one part per window; a provider that
// reports no window sent the word itself.
function usageText(usage: WindowUsage[] | "unknown"): string {
  if (usage === "unknown") {
    return "unknown";
  }
  const parts: string[] = [];
  for (const window of usage) {
    parts.push(`${window.name}: ${usd(window.used_usd)} of ${usd(window.limit_usd)}`);
  }
  return parts.join(" · ");
}

type Evidence = {
  // The rate as the endpoint prints it: a percentage, or the words for a rate
  // measured over none of the agent's lines at all.
  rate: string;
  prs: number[];
  agent_lines: number;
  owner_lines: number;
  review_found_lines: number;
  owner_change_lines: number;
  unlabelled_lines: number;
  reverts: number;
  unavailable: boolean;
};

type Earned = {
  level: number;
  stale: boolean;
  computed_at: string;
  evidence: Evidence;
};

type LevelChange = {
  at: string;
  from: number;
  to: number;
  cause: string;
  evidence: Evidence;
};

// The autonomy endpoint's own shape; earned is null while no snapshot is stored.
type AutonomyView = {
  level: number;
  allows: string;
  earned: Earned | null;
  history: LevelChange[];
};

type AutonomyState = {
  status: "loading" | "error" | "ready";
  view: AutonomyView;
};

// noView is what the section holds before the level is read and after a read
// that failed; it renders in neither state.
const noView: AutonomyView = { level: 0, allows: "", earned: null, history: [] };

// Autonomy shows the level the product runs at and what it allows, the PRs the
// level was earned on, and every recorded change of it. It reads only: the
// level is set with `runner autonomy -level`.
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
        throw new Error("the autonomy endpoint answered no level");
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

  const view = state.view;

  return (
    <section className="mx-auto max-w-desk px-pad py-band">
      <h1 className="text-title font-semibold text-ink">Autonomy</h1>
      <p className="mt-2 text-body text-ink2">What the product may do on its own.</p>
      <p className="mt-1 text-body text-ink2">Set with the runner autonomy command.</p>
      {state.status === "loading" && (
        <p role="status" className="mt-6">
          Reading the level…
        </p>
      )}
      {state.status === "error" && (
        <p role="alert" className="mt-6">
          The level could not be read.
        </p>
      )}
      {state.status === "ready" && (
        <>
          <p className="mt-6 font-mono text-title text-ink">Level {view.level}</p>
          <p className="mt-1 text-body text-ink2">{view.allows}</p>
          {levelNotes(view).map((note) => (
            <p key={note} className="mt-2 text-body text-defer">
              {note}
            </p>
          ))}
          <h2 className="mt-8 text-head font-semibold text-ink">Evidence</h2>
          {view.earned === null ? (
            <p className="mt-2 text-body text-ink2">No snapshot is stored.</p>
          ) : (
            <>
              <p className="mt-2 text-body text-ink2">Earned level {view.earned.level}.</p>
              {view.earned.evidence.unavailable && (
                <p className="mt-2 text-body text-defer">The evidence could not be read.</p>
              )}
              <div className="mt-4 overflow-x-auto">
                <table className="w-full text-body">
                  <caption className="sr-only">The evidence the level was computed from</caption>
                  <tbody>
                    {evidenceRows(view.earned).map((row) => (
                      <tr key={row.measure} className="border-b border-hair">
                        <th scope="row" className="py-2 pr-4 text-left font-normal text-ink2">
                          {row.measure}
                        </th>
                        <td className="py-2 font-mono">{row.value}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}
          <h2 className="mt-8 text-head font-semibold text-ink">History</h2>
          {view.history.length === 0 ? (
            <p className="mt-2 text-body text-ink2">No change is recorded.</p>
          ) : (
            <ul className="mt-2">
              {view.history.map((change, i) => (
                <li key={`${change.at}-${i}`} className="border-b border-hair py-3 text-body">
                  <p className="flex flex-wrap items-baseline gap-2">
                    <span className="font-mono">{`${change.from} → ${change.to}`}</span>
                    <span className={change.to < change.from ? "text-stop" : "text-ink2"}>
                      {changeKind(change)}
                    </span>
                    <span className="text-ink">{change.cause}</span>
                    <span className="text-label text-muted">
                      {new Date(change.at).toLocaleString()}
                    </span>
                  </p>
                  <p className="mt-1 text-label text-ink2">{evidenceLine(change.evidence)}</p>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </section>
  );
}

// isAutonomy narrows the endpoint's answer to the one thing this view can check
// about a document it did not build: that it carries a level and a history.
function isAutonomy(body: unknown): body is AutonomyView {
  if (typeof body !== "object" || body === null) {
    return false;
  }
  return "level" in body && Array.isArray(body.history);
}

// levelNotes is what the view says about a level the PRs have not earned: a
// stored level above the earned one runs at the earned one, and a snapshot that
// is stale or missing lets nothing auto-merge until it is recomputed.
function levelNotes(view: AutonomyView): string[] {
  const notes: string[] = [];
  if (view.earned === null) {
    notes.push("No snapshot is stored: nothing auto-merges until one is computed.");
    return notes;
  }
  if (view.earned.stale) {
    notes.push("The snapshot is stale: nothing auto-merges until it is recomputed.");
  }
  if (view.level > view.earned.level) {
    notes.push("The stored level is above the earned one, so the earned level runs.");
  }
  return notes;
}

// evidenceRows is the snapshot as the view's table lists it: when it was
// computed, and the evidence it was computed from.
function evidenceRows(earned: Earned): { measure: string; value: string }[] {
  const e = earned.evidence;
  const prs = e.prs.length === 0 ? "none" : e.prs.join(", ");
  return [
    { measure: "Computed", value: new Date(earned.computed_at).toLocaleString() },
    { measure: "Rewrite rate", value: e.rate },
    { measure: "PRs", value: prs },
    { measure: "Agent lines", value: String(e.agent_lines) },
    { measure: "Owner lines", value: String(e.owner_lines) },
    { measure: "Review-found lines", value: String(e.review_found_lines) },
    { measure: "Owner-change lines", value: String(e.owner_change_lines) },
    { measure: "Unlabelled lines", value: String(e.unlabelled_lines) },
    { measure: "Reverts", value: String(e.reverts) },
  ];
}

// evidenceLine is one change's evidence in a line: what it was measured on.
function evidenceLine(e: Evidence): string {
  if (e.unavailable) {
    return "The evidence could not be read.";
  }
  return `${e.rate} over ${e.prs.length} PRs, ${e.owner_lines} owner lines`;
}

// changeKind is the word the history marks a change with: a drop is the one that
// takes a level back, and the word carries it besides the colour.
function changeKind(change: LevelChange): string {
  if (change.to < change.from) {
    return "drop";
  }
  if (change.to > change.from) {
    return "raise";
  }
  return "unchanged";
}
