import { useState } from "react";
import { isBoolean, shape } from "../autonomy/guard";
import { postJSON } from "../post-json";
import { isRunsAtOnceView, parseRunsAtOnce } from "./runs-at-once";
import type { RunsAtOnceView } from "./runs-at-once";
import { PEAK_MODE, avoidsPeak, isPeakView } from "./types";
import type { PeakView } from "./types";

/**
 * The page's two switches and the runs-at-once field's text; a staged value is one the owner set
 * and has not saved.
 */
type Staged = { autoMerge?: boolean; avoidPeak?: boolean; runsAtOnce?: string };

type Switch = "autoMerge" | "avoidPeak";
type Setting = keyof Staged;

// Each setting's name in the words that report a save, in page order, which is the order it saves.
const SETTING_NAMES: Record<Setting, string> = {
  autoMerge: "Auto-merge",
  avoidPeak: "Avoid peak hours",
  runsAtOnce: "Runs at once",
};
const SAVE_ORDER: readonly Setting[] = ["autoMerge", "avoidPeak", "runsAtOnce"];

type Saved = {
  autoMerge: boolean | null;
  avoidPeak: boolean | null;
  runsAtOnce: RunsAtOnceView | null;
};

type Answers = {
  // The switch the auto-merge endpoint answered last, shown until the state reloads.
  autoMerge: boolean | null;
  peak: PeakView | null;
  runsAtOnce: RunsAtOnceView | null;
};

type Status = {
  saving: boolean;
  invalid: boolean;
  changed: boolean;
  // The N the last save set, until the next change.
  savedRunsAtOnce: number | null;
};

/** statusWords is the line under the page's settings: what a SAVE would do, or has just done. */
export function statusWords({ saving, invalid, changed, savedRunsAtOnce }: Status): string {
  if (saving) {
    return "Saving…";
  }
  if (invalid) {
    return "Fix the number to save.";
  }
  if (changed) {
    return "Change not saved yet.";
  }
  if (savedRunsAtOnce !== null) {
    return `Saved. Runs at once is now ${savedRunsAtOnce}.`;
  }
  return "No changes to save.";
}

/**
 * runsAtOnceChange is what the field's text stages against the saved N: no change while it reads
 * as the saved N, invalid while it holds no N the cap allows.
 */
export function runsAtOnceChange(
  text: string | undefined,
  saved: RunsAtOnceView | null,
): { changed: boolean; invalid: boolean } {
  if (text === undefined || saved === null) {
    return { changed: false, invalid: false };
  }
  const n = parseRunsAtOnce(text, saved.platform_cap);
  return { changed: n !== saved.runs_at_once, invalid: n === null };
}

const isEnabled = shape<{ enabled: boolean }>({ enabled: isBoolean });

// post sends body to path and returns the answer once guard has checked it.
async function post<T>(
  path: string,
  body: Record<string, string | number | boolean>,
  guard: (answer: unknown) => answer is T,
): Promise<T> {
  const response = await postJSON(path, body);
  if (!response.ok) {
    throw new Error(`${path} answered ${response.status}`);
  }
  const answer: unknown = await response.json();
  if (!guard(answer)) {
    throw new Error(`${path} answered a document of another shape`);
  }
  return answer;
}

// failureWords names what did not save and, where something did, what did.
function failureWords(failed: Setting[], saved: Setting[]): string {
  const names = (settings: Setting[]) => settings.map((s) => SETTING_NAMES[s]).join(" and ");
  const lost = `${names(failed)} could not be saved.`;
  return saved.length === 0 ? lost : `${lost} ${names(saved)} was saved.`;
}

/**
 * useConfigSave holds the switches and the runs-at-once text the owner staged and saves only what
 * differs from the saved value, one endpoint after the other in page order. A success adopts the
 * endpoint's answer and clears that value; a failure keeps it staged and the words name it.
 * onAutoMergeSaved re-reads the auto-merge state. Nothing saves while the field holds no N.
 */
export function useConfigSave(saved: Saved, onAutoMergeSaved: () => void) {
  const [staged, setStaged] = useState<Staged>({});
  const [answers, setAnswers] = useState<Answers>({
    autoMerge: null,
    peak: null,
    runsAtOnce: null,
  });
  const [saving, setSaving] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [savedRunsAtOnce, setSavedRunsAtOnce] = useState<number | null>(null);

  const current: Saved = {
    autoMerge: answers.autoMerge ?? saved.autoMerge,
    avoidPeak: answers.peak === null ? saved.avoidPeak : avoidsPeak(answers.peak.mode),
    runsAtOnce: answers.runsAtOnce ?? saved.runsAtOnce,
  };
  const field = runsAtOnceChange(staged.runsAtOnce, current.runsAtOnce);
  const changed = staged.autoMerge !== undefined || staged.avoidPeak !== undefined || field.changed;

  // stage sets a switch; setting it back to the saved value is no change at all.
  function stage(setting: Switch, value: boolean) {
    setFailure(null);
    setSavedRunsAtOnce(null);
    setStaged((was) => {
      const next = { ...was };
      if (value === current[setting]) {
        delete next[setting];
      } else {
        next[setting] = value;
      }
      return next;
    });
  }

  // toggle stages the opposite of what a switch shows.
  function toggle(setting: Switch) {
    stage(setting, !(staged[setting] ?? current[setting]));
  }

  // fill stages the field's text; it saves nothing.
  function fill(text: string) {
    setFailure(null);
    setSavedRunsAtOnce(null);
    setStaged((was) => ({ ...was, runsAtOnce: text }));
  }

  function clear(setting: Setting) {
    setStaged((was) => {
      const next = { ...was };
      delete next[setting];
      return next;
    });
  }

  async function save() {
    if (!changed || field.invalid || saving) {
      return;
    }
    setSaving(true);
    setFailure(null);
    const failed: Setting[] = [];
    const done: Setting[] = [];
    for (const setting of SAVE_ORDER) {
      if (staged[setting] === undefined || (setting === "runsAtOnce" && !field.changed)) {
        continue;
      }
      try {
        if (setting === "autoMerge") {
          const body = { enabled: staged.autoMerge === true };
          const { enabled } = await post("/api/auto-merge", body, isEnabled);
          setAnswers((was) => ({ ...was, autoMerge: enabled }));
          onAutoMergeSaved();
        } else if (setting === "avoidPeak") {
          const mode = staged.avoidPeak === true ? PEAK_MODE.offPeakOnly : PEAK_MODE.always;
          const view = await post("/api/peak", { mode }, isPeakView);
          setAnswers((was) => ({ ...was, peak: view }));
        } else {
          // field.changed holds only for text that reads as an N.
          const body = { runs_at_once: Number(staged.runsAtOnce?.trim()) };
          const view = await post("/api/runs-at-once", body, isRunsAtOnceView);
          setAnswers((was) => ({ ...was, runsAtOnce: view }));
          setSavedRunsAtOnce(view.runs_at_once);
        }
        clear(setting);
        done.push(setting);
      } catch (err: unknown) {
        console.error("configNotSaved", setting, err);
        failed.push(setting);
      }
    }
    setSaving(false);
    setFailure(failed.length === 0 ? null : failureWords(failed, done));
  }

  return {
    // What each control shows: the staged value, else the saved one.
    shown: {
      autoMerge: staged.autoMerge ?? current.autoMerge,
      avoidPeak: staged.avoidPeak ?? current.avoidPeak,
      runsAtOnce:
        staged.runsAtOnce ??
        (current.runsAtOnce === null ? null : String(current.runsAtOnce.runs_at_once)),
    },
    saved: current,
    changed,
    invalid: field.invalid,
    saving,
    failure,
    status: statusWords({ saving, invalid: field.invalid, changed, savedRunsAtOnce }),
    toggle,
    fill,
    save,
  };
}
