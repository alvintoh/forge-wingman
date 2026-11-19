import { useState } from "react";
import { isBoolean, shape } from "../autonomy/guard";
import { postJSON } from "../post-json";
import { isRunsAtOnceView } from "./runs-at-once";
import type { RunsAtOnceView } from "./runs-at-once";
import { PEAK_MODE, avoidsPeak, isPeakView, isPollIntervalView } from "./types";
import type { PeakView } from "./types";

/**
 * The page's two switches, the runs-at-once N and the poll interval; a staged value is one the
 * owner set and has not saved.
 */
type Staged = {
  autoMerge?: boolean;
  avoidPeak?: boolean;
  runsAtOnce?: number;
  pollMinutes?: number;
};

type Switch = "autoMerge" | "avoidPeak";
type Setting = keyof Staged;

// Each setting's name in the words that report a save, in page order, which is the order it saves.
const SETTING_NAMES: Record<Setting, string> = {
  autoMerge: "Auto-merge",
  avoidPeak: "Avoid peak hours",
  runsAtOnce: "Runs at once",
  pollMinutes: "Poll every",
};
const SAVE_ORDER: readonly Setting[] = ["autoMerge", "avoidPeak", "runsAtOnce", "pollMinutes"];

type Saved = {
  autoMerge: boolean | null;
  avoidPeak: boolean | null;
  runsAtOnce: RunsAtOnceView | null;
  pollMinutes: number | null;
};

type Answers = {
  // The switch the auto-merge endpoint answered last, shown until the state reloads.
  autoMerge: boolean | null;
  peak: PeakView | null;
  runsAtOnce: RunsAtOnceView | null;
  pollMinutes: number | null;
};

/** The values the last save set, each until the next change. */
type LastSaved = { runsAtOnce?: number; pollMinutes?: number };

type Status = {
  saving: boolean;
  changed: boolean;
  lastSaved: LastSaved;
};

// savedWords names each value the last save set, in page order.
function savedWords({ runsAtOnce, pollMinutes }: LastSaved): string[] {
  return [
    runsAtOnce === undefined ? null : `Runs at once is now ${runsAtOnce}`,
    pollMinutes === undefined ? null : `Poll every is now ${pollMinutes} minutes`,
  ].filter((words) => words !== null);
}

/** statusWords is the line under the page's settings: what a SAVE would do, or has just done. */
export function statusWords({ saving, changed, lastSaved }: Status): string {
  if (saving) {
    return "Saving…";
  }
  if (changed) {
    return "Change not saved yet.";
  }
  const saved = savedWords(lastSaved);
  if (saved.length > 0) {
    return `Saved. ${saved.join(" and ")}.`;
  }
  return "No changes to save.";
}

/** restage is was with setting staged at value; setting it back to the saved value is no change. */
export function restage<K extends Setting>(
  was: Staged,
  setting: K,
  value: Staged[K],
  savedValue: Staged[K] | null,
): Staged {
  const next = { ...was };
  if (value === savedValue) {
    delete next[setting];
  } else {
    next[setting] = value;
  }
  return next;
}

/** isChanged says whether anything is staged for a SAVE. */
export function isChanged(staged: Staged): boolean {
  return SAVE_ORDER.some((setting) => staged[setting] !== undefined);
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

/** One endpoint's answer to a save, named by the setting it saved. */
export type SavedAnswer =
  | { setting: "autoMerge"; enabled: boolean }
  | { setting: "avoidPeak"; view: PeakView }
  | { setting: "runsAtOnce"; view: RunsAtOnceView }
  | { setting: "pollMinutes"; minutes: number };

/** What the page holds of its settings between reads: what is staged, and what saves answered. */
export type Form = {
  staged: Staged;
  answers: Answers;
  lastSaved: LastSaved;
};

/** EMPTY_FORM is the page before any change or save. */
export const EMPTY_FORM: Form = {
  staged: {},
  answers: { autoMerge: null, peak: null, runsAtOnce: null, pollMinutes: null },
  lastSaved: {},
};

/**
 * withStaged is form with setting staged at value, or no longer staged once value is the saved one;
 * a change drops what the last save reported.
 */
export function withStaged<K extends Setting>(
  form: Form,
  setting: K,
  value: Staged[K],
  savedValue: Staged[K] | null,
): Form {
  return { ...form, staged: restage(form.staged, setting, value, savedValue), lastSaved: {} };
}

// unstage is staged without setting.
function unstage(staged: Staged, setting: Setting): Staged {
  const next = { ...staged };
  delete next[setting];
  return next;
}

/**
 * afterSave is form once answer has arrived: the answer adopted as the saved value, the setting no
 * longer staged, and a value the status line reports remembered.
 */
export function afterSave(form: Form, answer: SavedAnswer): Form {
  const staged = unstage(form.staged, answer.setting);
  const { answers, lastSaved } = form;
  switch (answer.setting) {
    case "autoMerge":
      return { ...form, staged, answers: { ...answers, autoMerge: answer.enabled } };
    case "avoidPeak":
      return { ...form, staged, answers: { ...answers, peak: answer.view } };
    case "runsAtOnce":
      return {
        staged,
        answers: { ...answers, runsAtOnce: answer.view },
        lastSaved: { ...lastSaved, runsAtOnce: answer.view.runs_at_once },
      };
    case "pollMinutes":
      return {
        staged,
        answers: { ...answers, pollMinutes: answer.minutes },
        lastSaved: { ...lastSaved, pollMinutes: answer.minutes },
      };
  }
}

/** currentOf is each setting's saved value: a save's answer, else what the page read. */
export function currentOf(saved: Saved, answers: Answers): Saved {
  return {
    autoMerge: answers.autoMerge ?? saved.autoMerge,
    avoidPeak: answers.peak === null ? saved.avoidPeak : avoidsPeak(answers.peak.mode),
    runsAtOnce: answers.runsAtOnce ?? saved.runsAtOnce,
    pollMinutes: answers.pollMinutes ?? saved.pollMinutes,
  };
}

/**
 * formView is what the page shows of form: each control's value (the staged one, else the saved
 * one), whether anything is staged, and the status line.
 */
export function formView(saved: Saved, form: Form, saving: boolean) {
  const current = currentOf(saved, form.answers);
  const { staged } = form;
  const changed = isChanged(staged);
  return {
    shown: {
      autoMerge: staged.autoMerge ?? current.autoMerge,
      avoidPeak: staged.avoidPeak ?? current.avoidPeak,
      runsAtOnce: staged.runsAtOnce ?? current.runsAtOnce?.runs_at_once ?? null,
      pollMinutes: staged.pollMinutes ?? current.pollMinutes,
    },
    saved: current,
    changed,
    status: statusWords({ saving, changed, lastSaved: form.lastSaved }),
  };
}

// failureWords names what did not save and, where something did, what did.
function failureWords(failed: Setting[], saved: Setting[]): string {
  const names = (settings: Setting[]) => settings.map((s) => SETTING_NAMES[s]).join(" and ");
  const lost = `${names(failed)} could not be saved.`;
  return saved.length === 0 ? lost : `${lost} ${names(saved)} was saved.`;
}

/**
 * saveStaged posts each staged setting to its endpoint, one after the other in page order, and
 * hands each answer to onSaved as it arrives. A failure is logged and the rest still save; it
 * answers which settings failed and which saved.
 */
export async function saveStaged(
  staged: Staged,
  onSaved: (answer: SavedAnswer) => void,
): Promise<{ failed: Setting[]; done: Setting[] }> {
  const failed: Setting[] = [];
  const done: Setting[] = [];
  for (const setting of SAVE_ORDER) {
    if (staged[setting] === undefined) {
      continue;
    }
    try {
      if (setting === "autoMerge") {
        const body = { enabled: staged.autoMerge === true };
        const { enabled } = await post("/api/auto-merge", body, isEnabled);
        onSaved({ setting, enabled });
      } else if (setting === "avoidPeak") {
        const mode = staged.avoidPeak === true ? PEAK_MODE.offPeakOnly : PEAK_MODE.always;
        onSaved({ setting, view: await post("/api/peak", { mode }, isPeakView) });
      } else if (setting === "runsAtOnce" && staged.runsAtOnce !== undefined) {
        const body = { runs_at_once: staged.runsAtOnce };
        onSaved({ setting, view: await post("/api/runs-at-once", body, isRunsAtOnceView) });
      } else if (setting === "pollMinutes" && staged.pollMinutes !== undefined) {
        const body = { poll_minutes: staged.pollMinutes };
        const view = await post("/api/poll-interval", body, isPollIntervalView);
        onSaved({ setting, minutes: view.poll_minutes });
      }
      done.push(setting);
    } catch (err: unknown) {
      console.error("configNotSaved", setting, err);
      failed.push(setting);
    }
  }
  return { failed, done };
}

/**
 * useConfigSave holds the switches, the runs-at-once N and the poll interval the owner staged and
 * saves only what differs from the saved value, one endpoint after the other in page order. A
 * success adopts the endpoint's answer and clears that value; a failure keeps it staged and the
 * words name it. onAutoMergeSaved re-reads the auto-merge state.
 */
export function useConfigSave(saved: Saved, onAutoMergeSaved: () => void) {
  const [form, setForm] = useState<Form>(EMPTY_FORM);
  const [saving, setSaving] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const view = formView(saved, form, saving);
  const current = view.saved;

  // stage sets a value and saves nothing.
  function stage<K extends Setting>(setting: K, value: Staged[K], savedValue: Staged[K] | null) {
    setFailure(null);
    setForm((was) => withStaged(was, setting, value, savedValue));
  }

  // toggle stages the opposite of what a switch shows.
  function toggle(setting: Switch) {
    stage(setting, !(form.staged[setting] ?? current[setting]), current[setting]);
  }

  // setRunsAtOnce stages N; it saves nothing.
  function setRunsAtOnce(n: number) {
    stage("runsAtOnce", n, current.runsAtOnce?.runs_at_once ?? null);
  }

  // setPollMinutes stages the poll interval; it saves nothing.
  function setPollMinutes(minutes: number) {
    stage("pollMinutes", minutes, current.pollMinutes);
  }

  function onSaved(answer: SavedAnswer) {
    setForm((was) => afterSave(was, answer));
    if (answer.setting === "autoMerge") {
      onAutoMergeSaved();
    }
  }

  async function save() {
    if (!view.changed || saving) {
      return;
    }
    setSaving(true);
    setFailure(null);
    const { failed, done } = await saveStaged(form.staged, onSaved);
    setSaving(false);
    setFailure(failed.length === 0 ? null : failureWords(failed, done));
  }

  return {
    ...view,
    saving,
    failure,
    toggle,
    setRunsAtOnce,
    setPollMinutes,
    save,
  };
}
