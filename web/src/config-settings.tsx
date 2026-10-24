import { useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { ActionButton } from "./action-button";

// The cap choices the modal offers, in order: the range the endpoint accepts.
const RUNS_PER_REPO = Array.from({ length: 20 }, (_, i) => i + 1);

// Settings is the settings endpoint's own answer: the auto-merge switch and
// whether it could be read, the owner's runs-per-repo cap and the GitHub job
// limit, which is read-only.
type Settings = {
  auto_merge: boolean;
  auto_merge_read: boolean;
  runs_per_repo: number;
  github_job_limit: number;
};

// isSettings checks the fields the modal reads have their declared types.
function isSettings(body: unknown): body is Settings {
  return (
    typeof body === "object" &&
    body !== null &&
    "auto_merge" in body &&
    typeof body.auto_merge === "boolean" &&
    "auto_merge_read" in body &&
    typeof body.auto_merge_read === "boolean" &&
    "runs_per_repo" in body &&
    typeof body.runs_per_repo === "number" &&
    "github_job_limit" in body &&
    typeof body.github_job_limit === "number"
  );
}

type ReadState = "loading" | "error" | "ready";

type ConfigSettingsProps = {
  open: boolean;
  onClose: () => void;
};

/**
 * SettingsButton is the header's "..." control: it opens Config settings, the
 * modal that holds the owner's auto-merge switch, the runs-per-repo cap and the
 * GitHub job limit the platform fixes.
 */
export function SettingsButton() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        type="button"
        aria-label="Settings"
        aria-haspopup="dialog"
        aria-expanded={open}
        onClick={() => setOpen(true)}
        className="group inline-flex size-11 shrink-0 cursor-pointer items-center justify-center text-control-icon"
      >
        <span className="grid size-8 place-items-center rounded-control border border-control-edge bg-control transition-colors duration-150 group-hover:bg-control-hover">
          {/* Octicons' kebab-horizontal-16, the "..." the drawing places here. */}
          <svg aria-hidden="true" width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
            <path d="M8 9a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3ZM1.5 9a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3Zm13 0a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3Z" />
          </svg>
        </span>
      </button>
      <ConfigSettings open={open} onClose={() => setOpen(false)} />
    </>
  );
}

// switchTrack and switchKnob are the switch's two parts, their fill or travel
// the only thing the setting changes.
function switchTrack(on: boolean): string {
  const base = "relative block h-6 w-11 rounded-full transition-colors duration-150";
  return `${base} ${on ? "bg-accent" : "bg-track"}`;
}

function switchKnob(on: boolean): string {
  const base =
    "absolute top-0.5 left-0.5 size-5 rounded-full bg-card shadow transition-transform duration-150";
  return `${base} ${on ? "translate-x-5" : ""}`;
}

/**
 * ConfigSettings is the Config settings modal: the auto-merge switch, the cap on
 * runs one repository may hold and the GitHub job limit, which no control here
 * changes. The first two write through their own endpoints, and every value is
 * read again each time the modal opens, so a reload shows what is stored.
 */
function ConfigSettings({ open, onClose }: ConfigSettingsProps) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const runsId = useId();
  const [status, setStatus] = useState<ReadState>("loading");
  const [autoMerge, setAutoMerge] = useState(false);
  const [autoMergeRead, setAutoMergeRead] = useState(true);
  const [savingMerge, setSavingMerge] = useState(false);
  const [mergeError, setMergeError] = useState("");
  const [runs, setRuns] = useState(RUNS_PER_REPO[0]);
  const [savedRuns, setSavedRuns] = useState(RUNS_PER_REPO[0]);
  const [savingRuns, setSavingRuns] = useState(false);
  const [runsError, setRunsError] = useState("");
  const [jobLimit, setJobLimit] = useState(0);

  useEffect(() => {
    const dialog = dialogRef.current;
    if (dialog === null) {
      return;
    }
    if (open && !dialog.open) {
      dialog.showModal();
    }
    if (!open && dialog.open) {
      dialog.close();
    }
  }, [open]);

  // Each opening reads the settings again: a value saved elsewhere, or by an
  // earlier visit, is what the modal shows.
  useEffect(() => {
    if (!open) {
      return;
    }
    const controller = new AbortController();
    setStatus("loading");
    setMergeError("");
    setRunsError("");

    async function read() {
      const response = await fetch("/api/settings", { signal: controller.signal });
      if (!response.ok) {
        throw new Error(`the settings endpoint answered ${response.status}`);
      }
      const body: unknown = await response.json();
      if (!isSettings(body)) {
        throw new Error("the settings endpoint answered no settings");
      }
      if (controller.signal.aborted) {
        return;
      }
      setAutoMerge(body.auto_merge);
      setAutoMergeRead(body.auto_merge_read);
      setRuns(body.runs_per_repo);
      setSavedRuns(body.runs_per_repo);
      setJobLimit(body.github_job_limit);
      setStatus("ready");
    }

    read().catch((err: unknown) => {
      if (controller.signal.aborted) {
        return;
      }
      console.error("settingsNotRead", err);
      setStatus("error");
    });

    return () => controller.abort();
  }, [open]);

  // The close event is queued after the dialog closes, so it can land after the
  // modal opened again; only a close that left the dialog shut is one.
  function handleClose() {
    if (dialogRef.current?.open !== true) {
      onClose();
    }
  }

  // A press on the dialog itself lands on its padding or, outside its box, on the scrim.
  function handleClick(event: React.MouseEvent<HTMLDialogElement>) {
    if (event.target !== event.currentTarget) {
      return;
    }
    const box = event.currentTarget.getBoundingClientRect();
    const inside =
      event.clientX >= box.left &&
      event.clientX <= box.right &&
      event.clientY >= box.top &&
      event.clientY <= box.bottom;
    if (!inside) {
      event.currentTarget.close();
    }
  }

  async function setAutoMergeSwitch(enabled: boolean) {
    setSavingMerge(true);
    setMergeError("");
    try {
      const response = await fetch("/api/auto-merge", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled }),
      });
      if (!response.ok) {
        throw new Error(`the auto-merge endpoint answered ${response.status}`);
      }
      setAutoMerge(enabled);
    } catch (err: unknown) {
      console.error("autoMergeSwitchNotSet", err);
      setMergeError("The switch could not be set.");
    } finally {
      setSavingMerge(false);
    }
  }

  async function saveRuns() {
    setSavingRuns(true);
    setRunsError("");
    try {
      const response = await fetch("/api/settings/runs-per-repo", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ runs_per_repo: runs }),
      });
      if (!response.ok) {
        throw new Error(`the settings endpoint answered ${response.status}`);
      }
      setSavedRuns(runs);
    } catch (err: unknown) {
      console.error("runsPerRepoNotSet", err);
      setRunsError("The cap could not be saved.");
    } finally {
      setSavingRuns(false);
    }
  }

  const mergeCaption = autoMergeRead
    ? "Merge an eligible PR when the rate allows it."
    : "The switch could not be read, so auto-merge is off.";

  return createPortal(
    <dialog
      ref={dialogRef}
      aria-labelledby={titleId}
      onClose={handleClose}
      onClick={handleClick}
      className="m-auto max-h-dvh w-full max-w-[min(28.75rem,calc(100dvw-2rem))] overflow-auto rounded-card border border-hair bg-card px-6.25 pt-[22.1px] pb-6 text-ink backdrop:bg-ink/45"
    >
      <div className="flex items-start justify-between gap-4">
        <h2 id={titleId} className="text-head leading-6 font-semibold">
          Config settings
        </h2>
        <button
          type="button"
          aria-label="Close settings"
          onClick={() => dialogRef.current?.close()}
          className="group -mt-2 -mr-2 inline-flex size-11 shrink-0 cursor-pointer items-center justify-center text-control-icon"
        >
          <span className="grid size-8 place-items-center rounded-control border border-control-edge bg-control transition-colors duration-150 group-hover:bg-control-hover">
            {/* Octicons' x-16. */}
            <svg aria-hidden="true" width="16" height="16" viewBox="0 0 16 16" fill="currentColor">
              <path d="M3.72 3.72a.75.75 0 0 1 1.06 0L8 6.94l3.22-3.22a.75.75 0 1 1 1.06 1.06L9.06 8l3.22 3.22a.75.75 0 1 1-1.06 1.06L8 9.06l-3.22 3.22a.75.75 0 0 1-1.06-1.06L6.94 8 3.72 4.78a.75.75 0 0 1 0-1.06Z" />
            </svg>
          </span>
        </button>
      </div>
      {status === "loading" && (
        <p role="status" className="mt-5 text-body text-ink2">
          Reading settings…
        </p>
      )}
      {status === "error" && (
        <p role="alert" className="mt-5 text-body text-stop">
          The settings could not be read.
        </p>
      )}
      {status === "ready" && (
        <ul className="mt-5">
          <li className="border-t border-hair py-4">
            <div className="flex items-center justify-between gap-4">
              <div className="min-w-0">
                <p className="text-body font-semibold text-ink">Auto-merge</p>
                <p className="mt-0.5 text-cap leading-4 text-muted">{mergeCaption}</p>
              </div>
              <button
                type="button"
                role="switch"
                aria-checked={autoMerge}
                aria-label="Auto-merge"
                disabled={savingMerge}
                onClick={() => setAutoMergeSwitch(!autoMerge)}
                className="inline-flex h-11 w-14 shrink-0 cursor-pointer items-center justify-center disabled:cursor-default disabled:opacity-50"
              >
                <span className={switchTrack(autoMerge)}>
                  <span className={switchKnob(autoMerge)} />
                </span>
              </button>
            </div>
            {mergeError !== "" && (
              <p role="alert" className="mt-2 text-cap text-stop">
                {mergeError}
              </p>
            )}
          </li>
          <li className="border-t border-hair py-4">
            <label htmlFor={runsId} className="text-body font-semibold text-ink">
              Runs per repo
            </label>
            <p className="mt-0.5 text-cap leading-4 text-muted">
              How many runs one repository may hold at once.
            </p>
            <div className="mt-3 flex items-center gap-3">
              <select
                id={runsId}
                value={runs}
                disabled={savingRuns}
                onChange={(event) => setRuns(Number(event.target.value))}
                className="h-11 rounded-tile border border-ink2 bg-ground pr-3 pl-3.25 text-head text-ink"
              >
                {RUNS_PER_REPO.map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </select>
              <ActionButton
                kind="primary"
                label="SAVE"
                onClick={saveRuns}
                disabled={savingRuns || runs === savedRuns}
              />
              {runsError !== "" ? (
                <p role="alert" className="text-cap text-stop">
                  {runsError}
                </p>
              ) : (
                <p role="status" className="text-cap text-muted">
                  {savingRuns ? "Saving…" : runs === savedRuns ? "Saved" : "Unsaved change"}
                </p>
              )}
            </div>
          </li>
          <li className="border-t border-hair py-4">
            <p className="text-body font-semibold text-ink">GitHub job limit</p>
            <p className="mt-0.5 text-cap leading-4 text-muted">CI shares it.</p>
            <p className="mt-3 text-body text-ink2">{jobLimit}</p>
          </li>
        </ul>
      )}
    </dialog>,
    document.body,
  );
}
