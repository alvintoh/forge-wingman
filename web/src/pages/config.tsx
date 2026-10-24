import { useCanGoBack, useNavigate, useRouter } from "@tanstack/react-router";
import { useId } from "react";
import { ActionButton, ButtonPair } from "../action-button";
import { isAutonomy } from "../autonomy/guard";
import { captionClass } from "../caption";
import { AutoMergeSection } from "../config/auto-merge-section";
import { ConcurrencySection } from "../config/concurrency-section";
import { ModelsSection } from "../config/models-section";
import { useConfigSave } from "../config/save-state";
import { avoidsPeak, isConfigView, isPeakView } from "../config/types";
import { useRead } from "../stats/use-read";

/**
 * Configuration holds every setting in one centred column, 598px at most: the auto-merge switch,
 * the models by phase with the Avoid peak hours switch, then the dispatcher's limits, read-only.
 * One BACK / SAVE pair ends the page and saves whichever switches changed.
 */
export function Configuration() {
  const autonomy = useRead("/api/autonomy", isAutonomy, "autonomyNotRead");
  const peak = useRead("/api/peak", isPeakView, "peakNotRead");
  const config = useRead("/api/config", isConfigView, "configNotRead");
  const form = useConfigSave(
    {
      autoMerge: autonomy.status === "ready" ? autonomy.body.auto_merge.switch_on : null,
      avoidPeak: peak.status === "ready" ? avoidsPeak(peak.body.mode) : null,
    },
    autonomy.reload,
  );
  const router = useRouter();
  const navigate = useNavigate();
  const canGoBack = useCanGoBack();
  const statusId = useId();

  function toggleAutoMerge() {
    form.toggle("autoMerge");
  }

  function toggleAvoidPeak() {
    form.toggle("avoidPeak");
  }

  function save() {
    void form.save();
  }

  function back() {
    if (canGoBack) {
      router.history.back();
    } else {
      void navigate({ to: "/" });
    }
  }

  return (
    <section aria-labelledby="config-title" className="px-padx pt-4 pb-band lg:pl-4">
      <div className="mx-auto max-w-[37.375rem]">
        <h1 id="config-title" className={`${captionClass} py-2 text-ink2`}>
          configuration
        </h1>
        <div className="card-frame mt-2">
          <AutoMergeSection
            read={autonomy}
            on={form.shown.autoMerge}
            disabled={form.saving}
            onToggle={toggleAutoMerge}
          />
          <ModelsSection
            config={config}
            peak={peak}
            avoidPeak={form.shown.avoidPeak}
            disabled={form.saving}
            onToggle={toggleAvoidPeak}
          />
          <ConcurrencySection config={config} />
        </div>
        <p
          id={statusId}
          role="status"
          className="mt-4 px-padx text-label leading-5 text-ink2 min-[600px]:px-0 min-[600px]:text-center"
        >
          {form.changed ? "Change not saved yet." : "No changes to save."}
        </p>
        {form.failure !== null && (
          <p
            role="alert"
            className="mt-1 px-padx text-label leading-5 text-stop min-[600px]:px-0 min-[600px]:text-center"
          >
            {form.failure}
          </p>
        )}
        <ButtonPair
          margin="mt-2.5"
          back={
            <ActionButton label="BACK" kind="secondary" disabled={form.saving} onClick={back} />
          }
          action={
            <ActionButton
              label="SAVE"
              kind="primary"
              aria-disabled={!form.changed || form.saving}
              aria-describedby={statusId}
              onClick={save}
            />
          }
        />
      </div>
    </section>
  );
}
