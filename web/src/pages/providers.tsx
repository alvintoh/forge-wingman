import { useEffect, useState } from "react";
import { cell } from "./shared";

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
    <section className="mx-auto max-w-desk px-padx pt-4 pb-band">
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
