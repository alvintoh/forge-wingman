import { useRead } from "./use-read";

type WindowUsage = {
  name: string;
  limit_usd: number;
  used_usd: number;
};

type Plan = {
  window_usage: WindowUsage[] | "unknown";
};

// The providers endpoint's own shape, as far as this view reads it; a plan whose
// provider reports no window carries the word "unknown" in place of its usage.
type ProvidersResponse = {
  plans: Plan[];
};

// Past this share of its limit a window is drawn as nearly spent.
const NEARLY_SPENT = 0.8;

const captionClass = "font-mono text-label font-semibold tracking-[1.4px] text-muted uppercase";

// Allowance draws the first plan that reports allowance windows, one gauge each
// with what is spent and what the limit is.
export function Allowance() {
  const read = useRead("/api/providers", isProviders, "allowanceNotRead");
  const windows = read.status === "ready" ? firstWindows(read.body.plans) : [];
  return (
    <section aria-labelledby="allowance-title">
      <h2 id="allowance-title" className={captionClass}>
        Allowance
      </h2>
      {read.status === "loading" && (
        <p role="status" className="mt-3 text-label text-ink2">
          Reading the allowance…
        </p>
      )}
      {read.status === "error" && (
        <p role="alert" className="mt-3 text-label text-ink2">
          The allowance could not be read.
        </p>
      )}
      {read.status === "ready" && windows.length === 0 && (
        <p className="mt-3 text-label text-ink2">No provider reports an allowance window.</p>
      )}
      {windows.length > 0 && (
        <ul className="mt-4 grid grid-cols-3 gap-3">
          {windows.map((usage) => (
            <li key={usage.name} className="min-w-0">
              <Gauge usage={usage} />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// firstWindows is the windows of the first plan, in the order a run walks the
// plans, whose provider reports any.
function firstWindows(plans: Plan[]): WindowUsage[] {
  for (const plan of plans) {
    if (plan.window_usage !== "unknown" && plan.window_usage.length > 0) {
      return plan.window_usage;
    }
  }
  return [];
}

function Gauge({ usage }: { usage: WindowUsage }) {
  const label = windowLabel(usage.name);
  // A window whose limit is not stated has no share to draw: dividing by it would
  // print NaN or an infinite share.
  const stated = Number.isFinite(usage.limit_usd) && usage.limit_usd > 0;
  const ratio = stated ? usage.used_usd / usage.limit_usd : 0;
  const percent = Math.round(ratio * 100);
  const nearlySpent = ratio > NEARLY_SPENT;
  const amount = stated
    ? `${usd(usage.used_usd)} / ${limit(usage.limit_usd)}`
    : `${usd(usage.used_usd)} spent`;
  return (
    <>
      <div className="flex items-baseline justify-between gap-2 font-mono text-cap">
        <span className="shrink-0 text-ink2">{label}</span>
        <span
          className={`min-w-0 text-right font-semibold ${nearlySpent ? "text-defer" : stated ? "text-ink" : "text-ink2"}`}
        >
          {stated ? `${percent}%` : "limit not stated"}
        </span>
      </div>
      <div
        role={stated ? "meter" : undefined}
        aria-label={stated ? `${label} allowance` : undefined}
        aria-valuemin={stated ? 0 : undefined}
        aria-valuemax={stated ? usage.limit_usd : undefined}
        aria-valuenow={stated ? Math.min(usage.used_usd, usage.limit_usd) : undefined}
        aria-valuetext={stated ? `${amount}, ${percent}%` : undefined}
        className="mt-1.5 h-1.5 overflow-hidden rounded-tag bg-track"
      >
        {usage.used_usd > 0 && stated && (
          <div
            className={`h-full rounded-tag ${nearlySpent ? "bg-defer" : "bg-accent"}`}
            style={{ width: `${Math.min(ratio, 1) * 100}%` }}
          />
        )}
      </div>
      <p className="mt-2 font-mono text-micro text-muted">
        {amount}
        {nearlySpent && (
          <span className="sr-only">, {ratio >= 1 ? "limit reached" : "nearly spent"}</span>
        )}
      </p>
    </>
  );
}

// windowLabel is the window's own name as the drawing labels it. A provider's
// windows are named for their span after the last dash in
// internal/providers/providers.json (command-code-goat-5h), so the label is the
// text after it, and the whole name when it has none.
function windowLabel(name: string): string {
  return name.slice(name.lastIndexOf("-") + 1);
}

function usd(amount: number): string {
  return `$${amount.toFixed(2)}`;
}

// limit prints a ceiling the way the drawing does: whole when it is whole.
function limit(amount: number): string {
  return Number.isInteger(amount) ? `$${amount}` : usd(amount);
}

// isProviders narrows the endpoint's answer to what this view reads: a list of
// plans that each carry their window usage, or the word for none.
function isProviders(body: unknown): body is ProvidersResponse {
  if (
    typeof body !== "object" ||
    body === null ||
    !("plans" in body) ||
    !Array.isArray(body.plans)
  ) {
    return false;
  }
  return body.plans.every(isPlan);
}

function isPlan(plan: unknown): plan is Plan {
  if (typeof plan !== "object" || plan === null || !("window_usage" in plan)) {
    return false;
  }
  const usage = plan.window_usage;
  return usage === "unknown" || (Array.isArray(usage) && usage.every(isWindowUsage));
}

function isWindowUsage(usage: unknown): usage is WindowUsage {
  return (
    typeof usage === "object" &&
    usage !== null &&
    "name" in usage &&
    typeof usage.name === "string" &&
    "limit_usd" in usage &&
    typeof usage.limit_usd === "number" &&
    "used_usd" in usage &&
    typeof usage.used_usd === "number"
  );
}
