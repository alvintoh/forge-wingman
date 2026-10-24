import { useState } from "react";
import { postJSON } from "../post-json";
import { PEAK_MODE, isPeakView } from "./types";
import type { PeakView } from "./types";

/** The page's two switches; a staged value is one the owner set and has not saved. */
export type Staged = { autoMerge?: boolean; avoidPeak?: boolean };

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
  // The switch the auto-merge endpoint answered last, so the page reads it before the state reloads.
  autoMerge: boolean | null;
  peak: PeakView | null;
};

function isEnabled(body: unknown): body is { enabled: boolean } {
  return (
    typeof body === "object" &&
    body !== null &&
    "enabled" in body &&
    typeof body.enabled === "boolean"
  );
}

async function postAutoMerge(enabled: boolean): Promise<boolean> {
  const response = await postJSON("/api/auto-merge", { enabled });
  if (!response.ok) {
    throw new Error(`/api/auto-merge answered ${response.status}`);
  }
  const body: unknown = await response.json();
  if (!isEnabled(body)) {
    throw new Error("/api/auto-merge answered a document of another shape");
  }
  return body.enabled;
}

async function postPeak(avoid: boolean): Promise<PeakView> {
  const mode = avoid ? PEAK_MODE.offPeakOnly : PEAK_MODE.always;
  const response = await postJSON("/api/peak", { mode });
  if (!response.ok) {
    throw new Error(`/api/peak answered ${response.status}`);
  }
  const body: unknown = await response.json();
  if (!isPeakView(body)) {
    throw new Error("/api/peak answered a document of another shape");
  }
  return body;
}

// failureWords names what did not save and, where something did, what did.
function failureWords(failed: Setting[], saved: Setting[]): string {
  const names = (settings: Setting[]) => settings.map((s) => SETTING_NAMES[s]).join(" and ");
  const lost = `${names(failed)} could not be saved.`;
  return saved.length === 0 ? lost : `${lost} ${names(saved)} was saved.`;
}

/**
 * useConfigSave holds the switches the owner staged and saves them: only what differs from the saved
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
    avoidPeak:
      answers.peak === null ? saved.avoidPeak : answers.peak.mode === PEAK_MODE.offPeakOnly,
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
          const enabled = await postAutoMerge(value);
          setAnswers((was) => ({ ...was, autoMerge: enabled }));
          onAutoMergeSaved();
        } else {
          const view = await postPeak(value);
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

  // discard drops every staged value.
  function discard() {
    setStaged({});
    setFailure(null);
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
    discard,
  };
}
