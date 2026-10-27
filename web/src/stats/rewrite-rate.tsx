import { RATE_DEFINITION, barOf } from "../autonomy/format";
import { isAutonomy } from "../autonomy/guard";
import { captionClass } from "../caption";
import { useProjectFilter } from "../project-filter";
import { PhaseModels, PhaseSlots } from "./phase-models";
import { useRead } from "./use-read";

// The key to the bar under each model: its median PR, on the 0 to 30% axis the bar is drawn on.
const BAR_KEY = "bar = median PR on the 0 to 30% axis";

/**
 * RewriteRate is the Statistics card for the rewrite rate: what it measures, the figure over the
 * trailing window against its limit, the key to the marks under each model, and the models of the
 * three phases.
 */
export function RewriteRate() {
  const read = useRead("/api/autonomy", isAutonomy, "autonomyNotRead");
  const view = read.status === "ready" ? read.body : null;
  const { project } = useProjectFilter();
  return (
    <section
      aria-labelledby="rewrite-rate-title"
      aria-busy={read.status === "loading"}
      className="card-frame py-4"
    >
      <h2 id="rewrite-rate-title" className={`${captionClass} text-ink2`}>
        rewrite rate
      </h2>
      <p className="mt-1.5 text-body text-ink2">{RATE_DEFINITION}</p>
      <div className="mt-1 min-h-4.5 text-label leading-4.5 text-ink2">
        {read.status === "loading" && <p role="status">Reading the rewrite rate…</p>}
        {read.status === "error" && <p role="alert">The rewrite rate could not be read.</p>}
        {view !== null && (
          <p>
            {`Rewrite rate over the trailing ${view.auto_merge.window} PRs is ${view.auto_merge.rate_value}; limit ${view.auto_merge.limit}`}
          </p>
        )}
      </div>
      <p className="mt-1 text-cap text-ink2">{BAR_KEY}</p>
      {project !== null && (
        <p className="mt-2 text-label font-semibold text-ink">rate and models: all projects</p>
      )}
      {read.status === "loading" && <PhaseSlots />}
      {view !== null && <PhaseModels figures={view.rewrite} bar={barOf(view.auto_merge.limit)} />}
    </section>
  );
}
