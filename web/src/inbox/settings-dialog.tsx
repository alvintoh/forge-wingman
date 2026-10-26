import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { ActionButton, ButtonPair } from "../action-button";
import { isAutonomy } from "../autonomy/guard";
import { autoMergeSentence } from "../config/auto-merge-sentence";
import { saveSetting } from "../config/save-setting";
import { Switch } from "../config/switch";
import { isAutoMergeAnswer, isConfigView, isRunsPerRepoView } from "../config/types";
import { useRead } from "../stats/use-read";

// The cap's bounds, which the dispatcher enforces (internal/dispatcher/admit.go).
const MIN_RUNS_PER_REPO = 1;
const MAX_RUNS_PER_REPO = 20;

const RUNS_SAYS = "How many runs of one repository may be in flight at once.";

type SettingsDialogProps = {
  onClose: () => void;
};

/**
 * SettingsDialog is the Config settings modal: the auto-merge switch, the runs-per-repo cap the
 * owner picks and saves, and the GitHub job limit the platform sets, read-only. It reads the three
 * endpoints afresh on every open, so a reload shows the saved value.
 */
export function SettingsDialog({ onClose }: SettingsDialogProps) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const mergeId = useId();
  const mergeSaysId = useId();
  const runsId = useId();
  const runsSaysId = useId();
  const autonomy = useRead("/api/autonomy", isAutonomy, "autonomyNotRead");
  const runs = useRead("/api/runs-per-repo", isRunsPerRepoView, "runsPerRepoNotRead");
  const config = useRead("/api/config", isConfigView, "configNotRead");
  // The switch and the cap the owner has changed and not saved, and the ones the endpoints
  // answered, which stand until this opening closes.
  const [staged, setStaged] = useState<boolean | undefined>(undefined);
  const [typed, setTyped] = useState<string | undefined>(undefined);
  const [answered, setAnswered] = useState<boolean | undefined>(undefined);
  const [answeredRuns, setAnsweredRuns] = useState<number | undefined>(undefined);
  const [saving, setSaving] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog !== null && !dialog.open) {
      dialog.showModal();
    }
  }, []);

  const saved =
    answered ?? (autonomy.status === "ready" ? autonomy.body.auto_merge.switch_on : null);
  const shown = staged ?? saved;
  const savedRuns = answeredRuns ?? (runs.status === "ready" ? runs.body.runs_per_repo : null);
  const typedRuns = typed ?? (savedRuns === null ? "" : `${savedRuns}`);
  const typedNumber = Number(typedRuns);
  let picked: number | null = null;
  if (
    Number.isInteger(typedNumber) &&
    typedNumber >= MIN_RUNS_PER_REPO &&
    typedNumber <= MAX_RUNS_PER_REPO
  ) {
    picked = typedNumber;
  }
  const changed =
    staged !== undefined || (savedRuns !== null && picked !== null && picked !== savedRuns);
  // A typed cap outside the range holds the save: closing would drop it without a word.
  const invalidCap = savedRuns !== null && picked === null;

  const merge = autonomy.status === "ready" ? autonomy.body.auto_merge : null;
  let mergeSays: string;
  if (autonomy.status === "loading") {
    mergeSays = "Reading the auto-merge state…";
  } else if (merge === null) {
    mergeSays = "The auto-merge state could not be read.";
  } else {
    mergeSays = autoMergeSentence(shown === true, merge);
  }

  let runsSays = RUNS_SAYS;
  if (runs.status === "loading") {
    runsSays = "Reading the saved value…";
  } else if (runs.status === "error") {
    runsSays = "The saved value could not be read.";
  } else if (picked === null) {
    runsSays = `Enter a whole number from ${MIN_RUNS_PER_REPO} to ${MAX_RUNS_PER_REPO}.`;
  }

  const limit = config.status === "ready" ? `${config.body.concurrency.platform_cap}` : "…";

  function toggleMerge() {
    if (shown === null) {
      return;
    }
    setFailure(null);
    const next = !shown;
    setStaged(next === saved ? undefined : next);
  }

  function changeRuns(event: React.ChangeEvent<HTMLInputElement>) {
    setFailure(null);
    setTyped(event.target.value);
  }

  // Every close goes through the dialog, so the browser hands the focus back to the settings
  // button that opened it before this component unmounts.
  function close() {
    dialogRef.current?.close();
  }

  // The close event is queued after the dialog closes, so it can land after the dialog opened
  // again; only a close that left it shut is one.
  function handleClose() {
    if (dialogRef.current?.open !== true) {
      onClose();
    }
  }

  async function save() {
    if (saving || !changed) {
      return;
    }
    setSaving(true);
    setFailure(null);
    const failed: string[] = [];
    if (staged !== undefined) {
      try {
        const answer = await saveSetting("/api/auto-merge", { enabled: staged }, isAutoMergeAnswer);
        setAnswered(answer.enabled);
        setStaged(undefined);
      } catch (err: unknown) {
        console.error("autoMergeNotSaved", err);
        failed.push("Auto-merge");
      }
    }
    if (savedRuns !== null && picked !== null && picked !== savedRuns) {
      try {
        const answer = await saveSetting(
          "/api/runs-per-repo",
          { runs_per_repo: picked },
          isRunsPerRepoView,
        );
        setAnsweredRuns(answer.runs_per_repo);
        setTyped(undefined);
      } catch (err: unknown) {
        console.error("runsPerRepoNotSaved", err);
        failed.push("Runs per repo");
      }
    }
    setSaving(false);
    if (failed.length > 0) {
      setFailure(`${failed.join(" and ")} could not be saved.`);
      return;
    }
    close();
  }

  // In the body, so the inbox section moving between columns never takes the open modal with it.
  return createPortal(
    <dialog
      ref={dialogRef}
      aria-labelledby={titleId}
      onClose={handleClose}
      className="m-auto max-h-dvh w-full max-w-[min(28.75rem,calc(100dvw-2rem))] overflow-auto rounded-card border border-hair bg-card px-6.25 pt-7.25 pb-5.25 text-ink backdrop:bg-ink/45"
    >
      <h2 id={titleId} className="text-head leading-6 font-semibold">
        Config settings
      </h2>
      <div className="mt-2">
        <div className="border-t border-hair pt-3">
          <div className="flex min-h-11 items-center justify-between gap-3">
            <span id={mergeId} className="text-body font-semibold text-ink">
              Auto-merge
            </span>
            <Switch
              on={shown}
              labelledBy={mergeId}
              describedBy={mergeSaysId}
              disabled={saving}
              onToggle={toggleMerge}
            />
          </div>
          <p id={mergeSaysId} className="pb-2 text-label leading-5 text-ink2">
            {mergeSays}
          </p>
        </div>
        <div className="border-t border-hair pt-3">
          <div className="flex min-h-11 items-center justify-between gap-3">
            <label htmlFor={runsId} className="text-body font-semibold text-ink">
              Runs per repo
            </label>
            <input
              id={runsId}
              type="number"
              inputMode="numeric"
              min={MIN_RUNS_PER_REPO}
              max={MAX_RUNS_PER_REPO}
              step={1}
              value={typedRuns}
              disabled={saving || savedRuns === null}
              onChange={changeRuns}
              aria-describedby={runsSaysId}
              className="h-11 w-20 rounded-control border border-hair bg-card px-3 text-right text-body font-semibold text-ink focus-visible:outline-3 focus-visible:outline-offset-2 focus-visible:outline-accent disabled:bg-track disabled:text-muted"
            />
          </div>
          <p id={runsSaysId} className="pb-2 text-label leading-5 text-ink2">
            {runsSays}
          </p>
        </div>
        <div className="border-t border-hair pt-3">
          <div className="flex min-h-11 items-center justify-between gap-3">
            <span className="text-body font-semibold text-ink">GitHub job limit</span>
            <span className="text-body font-semibold text-ink">{limit}</span>
          </div>
          <p className="pb-2 text-label leading-5 text-muted">CI shares it.</p>
        </div>
      </div>
      {failure !== null && (
        <p role="alert" className="mt-3 text-label leading-5 text-stop">
          {failure}
        </p>
      )}
      <ButtonPair
        margin="mt-4"
        back={<ActionButton kind="secondary" label="CLOSE" onClick={close} />}
        action={
          <ActionButton
            kind="primary"
            label="SAVE"
            onClick={save}
            disabled={saving || !changed || invalidCap}
          />
        }
      />
    </dialog>,
    document.body,
  );
}
