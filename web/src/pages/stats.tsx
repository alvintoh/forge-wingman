import { useEffect, useState } from "react";
import { Allowance } from "../stats/allowance";
import { AutoMerge } from "../stats/auto-merge";
import { RunHistory } from "../stats/run-history";

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
    <section className="mx-auto max-w-desk px-padx pt-4 pb-band">
      <h1 className="text-title font-semibold text-ink">Statistics</h1>
      {/* The band splits on the seam the Overview's columns do. */}
      <div className="mt-4 mb-8 grid grid-cols-1 gap-y-6 @[51.25rem]:grid-cols-[minmax(0,1fr)_23rem] @[51.25rem]:gap-x-8">
        <RunHistory />
        <Allowance />
        <AutoMerge />
      </div>
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
