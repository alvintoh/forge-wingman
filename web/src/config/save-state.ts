import { useState } from "react";
import { isBoolean, shape } from "../autonomy/guard";
import { postJSON } from "../post-json";
import { PEAK_MODE, avoidsPeak, isPeakView } from "./types";
import type { PeakView } from "./types";

/** The page's two switches; a staged value is one the owner set and has not saved. */
type Staged = { autoMerge?: boolean; avoidPeak?: boolean };

type Setting = keyof Staged;

// Each setting's name in the words that report a save, in page order, which is the order it saves.
const SETTING_NAMES: Record<Setting, string> = {
  autoMerge: "Auto-merge",
  avoidPeak: "Avoid peak hours",
};
const SAVE_ORDER: readonly Setting[] = ["autoMerge", "avoidPeak"];

type Saved = {
  autoMerge: boolean | null;
  avoidPeak: boolean | null;
};

type Answers = {
  // The switch the auto-merge endpoint answered last, shown until the state reloads.
  autoMerge: boolean | null;
  peak: PeakView | null;
};

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
 * useConfigSave holds the switches the owner staged and saves only what differs from the saved
 * value, one endpoint after the other in page order. A success adopts the endpoint's answer and
 * clears that value; a failure keeps it staged and the words name it. onAutoMergeSaved re-reads
 * the auto-merge state.
 */
export function useConfigSave(saved: Saved, onAutoMergeSaved: () => void) {
  const [staged, setStaged] = useState<Staged>({});
  const [answers, setAnswers] = useState<Answers>({ autoMerge: null, peak: null });
  const [saving, setSaving] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

  const current: Saved = {
    autoMerge: answers.autoMerge ?? saved.autoMerge,
    avoidPeak: answers.peak === null ? saved.avoidPeak : avoidsPeak(answers.peak.mode),
  };
  const changed = SAVE_ORDER.some((setting) => staged[setting] !== undefined);

  // stage sets a switch; setting it back to the saved value is no change at all.
  function stage(setting: Setting, value: boolean) {
    setFailure(null);
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
  function toggle(setting: Setting) {
    stage(setting, !(staged[setting] ?? current[setting]));
  }

  function clear(setting: Setting) {
    setStaged((was) => {
      const next = { ...was };
      delete next[setting];
      return next;
    });
  }

  async function save() {
    if (!changed || saving) {
      return;
    }
    setSaving(true);
    setFailure(null);
    const failed: Setting[] = [];
    const done: Setting[] = [];
    for (const setting of SAVE_ORDER) {
      const value = staged[setting];
      if (value === undefined) {
        continue;
      }
      try {
        if (setting === "autoMerge") {
          const { enabled } = await post("/api/auto-merge", { enabled: value }, isEnabled);
          setAnswers((was) => ({ ...was, autoMerge: enabled }));
          onAutoMergeSaved();
        } else {
          const mode = value ? PEAK_MODE.offPeakOnly : PEAK_MODE.always;
          const view = await post("/api/peak", { mode }, isPeakView);
          setAnswers((was) => ({ ...was, peak: view }));
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
    // What each switch shows: the staged value, else the saved one.
    shown: {
      autoMerge: staged.autoMerge ?? current.autoMerge,
      avoidPeak: staged.avoidPeak ?? current.avoidPeak,
    },
    saved: current,
    changed,
    saving,
    failure,
    toggle,
    save,
  };
}
