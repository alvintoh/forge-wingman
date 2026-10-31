import { barOf, reasonText, whole } from "../autonomy/format";
import { isAutonomy, isRewrite } from "../autonomy/guard";
import type { MergeState } from "../autonomy/types";
import { captionClass } from "../caption";
import { PhaseModels, PhaseSlots } from "./phase-models";
import { useRead } from "./use-read";

const DEFINITION =
  "Rewrite rate is owner lines as a share of agent lines in merged PRs. Lower is better.";
const KEY =
  "Median PR is the middle PR's own rewrite rate. Pooled is the same rate over all of a model's lines.";

// The sentence names the window and the figure only where the endpoint measured both.
function rateSentence(merge: MergeState): string {
  if (!merge.rate_value.endsWith("%") || merge.reason === "too-few-measured") {
    return reasonText(merge.reason);
  }
  return `Rewrite rate over the trailing ${merge.window} PRs is ${merge.rate_value}; guide ${whole(barOf(merge.limit))}`;
}

/**
 * RewriteRate is the Statistics card for the rewrite rate: what it measures, the figure over the
 * trailing window against its guide, the key to the figures under each model, and the models of
 * the three phases. The endpoint is pooled, so a project chosen on the page leaves every figure
 * here as All draws it and the card says so.
 */
export function RewriteRate({ projectChosen }: { projectChosen: boolean }) {
  const read = useRead("/api/autonomy", isAutonomy, "autonomyNotRead");
  const view = read.status === "ready" ? read.body : null;
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
        {view !== null && <p>{rateSentence(view.auto_merge)}</p>}
      </div>
      {view !== null && projectChosen && (
        <p className="mt-2 text-label font-semibold text-ink">rate and models: all projects</p>
      )}
      {view !== null && isRewrite(view.rewrite) && (
        <p className="mt-3.5 border-t border-hair pt-2 text-cap text-ink2">{KEY}</p>
      )}
      {read.status === "loading" && <PhaseSlots />}
      {view !== null && <PhaseModels figures={view.rewrite} bar={barOf(view.auto_merge.limit)} />}
    </section>
  );
}
