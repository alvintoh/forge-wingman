import { afterEach, beforeEach, describe, expect, it, mock, spyOn } from "bun:test";
import type { SavedAnswer } from "./save-state";
import {
  EMPTY_FORM,
  afterSave,
  formView,
  isChanged,
  restage,
  saveStaged,
  statusWords,
  withStaged,
} from "./save-state";

const realFetch = globalThis.fetch;
let errors: ReturnType<typeof spyOn>;

beforeEach(() => {
  errors = spyOn(console, "error").mockImplementation(() => {});
});

afterEach(() => {
  globalThis.fetch = realFetch;
  errors.mockRestore();
});

function answering(status: number, body: unknown) {
  const fetchSpy = mock((_url: string, _init?: RequestInit) =>
    Promise.resolve(new Response(JSON.stringify(body), { status })),
  );
  globalThis.fetch = fetchSpy as unknown as typeof fetch;
  return fetchSpy;
}

function handlers() {
  return mock((_answer: SavedAnswer) => {});
}

describe("staging Poll every", () => {
  it("stages the other segment as a change and drops it once the saved one is chosen again", () => {
    const staged = restage({}, "pollMinutes", 10, 15);
    expect(staged).toEqual({ pollMinutes: 10 });
    expect(isChanged(staged)).toBe(true);
    expect(statusWords({ saving: false, changed: true, lastSaved: {} })).toBe(
      "Change not saved yet.",
    );
    expect(isChanged(restage(staged, "pollMinutes", 15, 15))).toBe(false);
  });
});

describe("saveStaged", () => {
  it("posts a staged interval to the poll-interval endpoint and hands back its answer", async () => {
    const fetchSpy = answering(200, { poll_minutes: 10 });
    const on = handlers();
    const result = await saveStaged({ pollMinutes: 10 }, on);
    expect(fetchSpy.mock.calls.map(([url, init]) => [url, init?.method, init?.body])).toEqual([
      ["/api/poll-interval", "POST", '{"poll_minutes":10}'],
    ]);
    expect(on.mock.calls).toEqual([[{ setting: "pollMinutes", minutes: 10 }]]);
    expect(result).toEqual({ failed: [], done: ["pollMinutes"] });
  });

  it("posts nothing for an interval that is not staged", async () => {
    const fetchSpy = answering(200, { enabled: true });
    const on = handlers();
    await saveStaged({ autoMerge: true }, on);
    expect(fetchSpy.mock.calls.map(([url]) => url)).toEqual(["/api/auto-merge"]);
    expect(on.mock.calls).toEqual([[{ setting: "autoMerge", enabled: true }]]);
  });

  it("keeps the interval failed when the endpoint refuses it or answers another shape", async () => {
    for (const [status, body] of [
      [400, {}],
      [200, { minutes: 10 }],
    ] as const) {
      answering(status, body);
      const on = handlers();
      expect(await saveStaged({ pollMinutes: 10 }, on)).toEqual({
        failed: ["pollMinutes"],
        done: [],
      });
      expect(on).not.toHaveBeenCalled();
    }
    expect(errors).toHaveBeenCalledWith("configNotSaved", "pollMinutes", expect.any(Error));
  });
});

const SAVED = { autoMerge: true, avoidPeak: true, runsAtOnce: null, pollMinutes: 15 };

describe("a save of Poll every", () => {
  it("leaves nothing staged, shows the saved interval and says what it saved", async () => {
    answering(200, { poll_minutes: 10 });
    let form = withStaged(EMPTY_FORM, "pollMinutes", 10, 15);
    expect(formView(SAVED, form, false).changed).toBe(true);
    await saveStaged(form.staged, (answer) => {
      form = afterSave(form, answer);
    });
    const view = formView(SAVED, form, false);
    expect(view.changed).toBe(false);
    expect(view.shown.pollMinutes).toBe(10);
    expect(view.saved.pollMinutes).toBe(10);
    expect(view.status).toBe("Saved. Poll every is now 10 minutes.");
  });

  it("stops saying what it saved at the next change", () => {
    const saved = afterSave(EMPTY_FORM, { setting: "pollMinutes", minutes: 10 });
    const next = withStaged(saved, "pollMinutes", 15, 10);
    expect(formView(SAVED, next, false).status).toBe("Change not saved yet.");
    expect(formView(SAVED, withStaged(next, "pollMinutes", 10, 10), false).status).toBe(
      "No changes to save.",
    );
  });
});

describe("statusWords after a save", () => {
  it("names both values when runs at once and Poll every saved together", () => {
    expect(
      statusWords({
        saving: false,
        changed: false,
        lastSaved: { runsAtOnce: 3, pollMinutes: 10 },
      }),
    ).toBe("Saved. Runs at once is now 3 and Poll every is now 10 minutes.");
  });
});
