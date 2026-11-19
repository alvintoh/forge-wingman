import { useCanGoBack, useNavigate, useRouter } from "@tanstack/react-router";
import { useId } from "react";
import { ActionButton, ButtonPair } from "../action-button";
import { isAutonomy } from "../autonomy/guard";
import type { AutonomyView } from "../autonomy/types";
import { captionClass } from "../caption";
import { AutoMergeSection } from "../config/auto-merge-section";
import { ConcurrencySection } from "../config/concurrency-section";
import { ModelsSection } from "../config/models-section";
import { isRunsAtOnceView } from "../config/runs-at-once";
import type { RunsAtOnceView } from "../config/runs-at-once";
import { RunsAtOnceSection } from "../config/runs-at-once-section";
import { useConfigSave } from "../config/save-state";
import {
  avoidsPeak,
  isConfigView,
  isPeakView,
  isPollIntervalView,
  isRunsPerRepoView,
} from "../config/types";
import type { ConfigView, PeakView, PollIntervalView, RunsPerRepoView } from "../config/types";
import { useRead } from "../stats/use-read";
import type { Read } from "../stats/use-read";

type SettingsCardProps = {
  autonomy: Read<AutonomyView>;
  peak: Read<PeakView>;
  config: Read<ConfigView>;
  runsPerRepo: Read<RunsPerRepoView>;
  runsAtOnce: Read<RunsAtOnceView>;
  pollInterval: Read<PollIntervalView>;
  form: Pick<
    ReturnType<typeof useConfigSave>,
    "shown" | "saved" | "saving" | "toggle" | "setRunsAtOnce" | "setPollMinutes"
  >;
};

/** SettingsCard is the page's one card: each section, showing what form holds of its setting. */
export function SettingsCard({
  autonomy,
  peak,
  config,
  runsPerRepo,
  runsAtOnce,
  pollInterval,
  form,
}: SettingsCardProps) {
  function toggleAutoMerge() {
    form.toggle("autoMerge");
  }

  function toggleAvoidPeak() {
    form.toggle("avoidPeak");
  }

  return (
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
      <RunsAtOnceSection
        read={runsAtOnce}
        view={form.saved.runsAtOnce}
        value={form.shown.runsAtOnce}
        disabled={form.saving}
        onChange={form.setRunsAtOnce}
      />
      <ConcurrencySection
        config={config}
        runsPerRepo={runsPerRepo}
        pollInterval={pollInterval}
        pollMinutes={form.shown.pollMinutes}
        disabled={form.saving}
        onPollChoose={form.setPollMinutes}
      />
    </div>
  );
}

/**
 * Configuration holds every setting in one centred column, 598px at most: the auto-merge switch,
 * the models by phase with the Avoid peak hours switch, runs at once (N), then the dispatcher's
 * other limits, read-only but for the poll interval. One BACK / SAVE pair ends the page and saves
 * whatever changed.
 */
export function Configuration() {
  const autonomy = useRead("/api/autonomy", isAutonomy, "autonomyNotRead");
  const peak = useRead("/api/peak", isPeakView, "peakNotRead");
  const config = useRead("/api/config", isConfigView, "configNotRead");
  const runsPerRepo = useRead("/api/runs-per-repo", isRunsPerRepoView, "runsPerRepoNotRead");
  const runsAtOnce = useRead("/api/runs-at-once", isRunsAtOnceView, "runsAtOnceNotRead");
  const pollInterval = useRead("/api/poll-interval", isPollIntervalView, "pollIntervalNotRead");
  const form = useConfigSave(
    {
      autoMerge: autonomy.status === "ready" ? autonomy.body.auto_merge.switch_on : null,
      avoidPeak: peak.status === "ready" ? avoidsPeak(peak.body.mode) : null,
      runsAtOnce: runsAtOnce.status === "ready" ? runsAtOnce.body : null,
      pollMinutes: pollInterval.status === "ready" ? pollInterval.body.poll_minutes : null,
    },
    autonomy.reload,
  );
  const router = useRouter();
  const navigate = useNavigate();
  const canGoBack = useCanGoBack();
  const statusId = useId();

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
        <SettingsCard
          autonomy={autonomy}
          peak={peak}
          config={config}
          runsPerRepo={runsPerRepo}
          runsAtOnce={runsAtOnce}
          pollInterval={pollInterval}
          form={form}
        />
        <p
          id={statusId}
          role="status"
          className="mt-4 px-padx text-label leading-5 text-ink2 min-[600px]:px-0 min-[600px]:text-center"
        >
          {form.status}
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
