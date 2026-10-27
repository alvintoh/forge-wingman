import { barOf } from "../autonomy/format";
import { isAutonomy } from "../autonomy/guard";
import { captionClass } from "../caption";
import { useKnownProject } from "../project-filter";
import { useProjects } from "../project-list";
import { PhaseModels, PhaseSlots } from "./phase-models";
import { useRead } from "./use-read";

const DEFINITION =
  "Rewrite rate is owner lines as a share of agent lines in merged PRs. Lower is better.";
const KEY =
  "Median PR is the middle PR's own rewrite rate. Pooled is the same rate over all of a model's lines.";

/**
 * RewriteRate is the Statistics card for the rewrite rate: what it measures, the figure over the
 * trailing window against its limit, the key to the figures under each model, and the models of
 * the three phases.
 */
export function RewriteRate() {
  const read = useRead("/api/autonomy", isAutonomy, "autonomyNotRead");
  const view = read.status === "ready" ? read.body : null;
  const { state: list } = useProjects();
  const { known } = useKnownProject(list.projects, list.status === "ready");
  return (
    <section
      aria-labelledby="rewrite-rate-title"
      aria-busy={read.status === "loading"}
      className="card-frame py-4"
    >
      <h2 id="rewrite-rate-title" className={`${captionClass} text-ink2`}>
        rewrite rate
      </h2>
      <p className="mt-1.5 text-cap text-ink2">{DEFINITION}</p>
      <div className="mt-1 min-h-4.5 text-label leading-4.5 text-ink2">
        {read.status === "loading" && <p role="status">Reading the rewrite rate…</p>}
        {read.status === "error" && <p role="alert">The rewrite rate could not be read.</p>}
        {view !== null && (
          <p>
            {`Rewrite rate over the trailing ${view.auto_merge.window} PRs is ${view.auto_merge.rate_value}; limit ${view.auto_merge.limit}`}
          </p>
        )}
      </div>
      {view !== null && known !== undefined && (
        <p className="mt-2 text-label font-semibold text-ink">rate and models: all projects</p>
      )}
      {view !== null && (
        <p className="mt-3.5 border-t border-hair pt-4 text-cap text-ink2">{KEY}</p>
      )}
      {read.status === "loading" && <PhaseSlots />}
      {view !== null && <PhaseModels figures={view.rewrite} bar={barOf(view.auto_merge.limit)} />}
    </section>
  );
}
