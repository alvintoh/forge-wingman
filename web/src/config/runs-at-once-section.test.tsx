import { afterEach, describe, expect, it, mock } from "bun:test";
import { isValidElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import type { Recommendation, RunsAtOnceView } from "./runs-at-once";
import { RunsAtOnceSection, Stepper } from "./runs-at-once-section";
import { isChanged, restage, statusWords } from "./save-state";

const RULE = { stable_runs: 5, rise_within: 1.25, halve_beyond: 1.5 };

function view(saved: number, r: Partial<Recommendation> | null): RunsAtOnceView {
  return {
    runs_at_once: saved,
    max: 15,
    recommendation: r === null ? null : { n: saved, basis: "none", stable: 0, ...RULE, ...r },
  };
}

function draw(v: RunsAtOnceView, value: number, disabled = false): string {
  return renderToStaticMarkup(
    <RunsAtOnceSection
      read={{ status: "ready", body: v, url: "/api/runs-at-once" }}
      view={v}
      value={value}
      disabled={disabled}
      onChange={() => {}}
    />,
  );
}

type Found = Record<string, unknown>;

// propsOf is every element's props in a tree whose prop key holds value.
function propsOf(node: ReactNode, key: string, value: unknown): Found[] {
  if (Array.isArray(node)) {
    return node.flatMap((child: ReactNode) => propsOf(child, key, value));
  }
  if (!isValidElement<Found>(node)) {
    return [];
  }
  const own = node.props[key] === value ? [node.props] : [];
  return [...own, ...propsOf(node.props.children as ReactNode, key, value)];
}

function stepper(value: number, onChange: (n: number) => void, disabled = false) {
  const tree = Stepper({ value, max: 15, disabled, onChange });
  const [more] = propsOf(tree, "name", "More runs at once");
  const [fewer] = propsOf(tree, "name", "Fewer runs at once");
  const [spin] = propsOf(tree, "role", "spinbutton");
  return {
    more: more as { live: boolean; onStep: () => void },
    fewer: fewer as { live: boolean; onStep: () => void },
    // key presses key on the value and says whether it kept the page from scrolling.
    key: (key: string) => {
      let prevented = false;
      (spin.onKeyDown as (e: { key: string; preventDefault: () => void }) => void)({
        key,
        preventDefault: () => {
          prevented = true;
        },
      });
      return prevented;
    },
  };
}

const realFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = realFetch;
});

describe("the stepper", () => {
  it("shows the name, then the value as a 1 to 15 spinbutton with + over − at its right", () => {
    const html = draw(view(2, { n: 2 }), 3);
    expect(html).toContain("Runs at once (N)");
    expect(html).toMatch(
      /role="spinbutton"[^>]*aria-valuemin="1" aria-valuemax="15" aria-valuenow="3"[^>]*class="[^"]*text-body font-semibold[^>]*>3</,
    );
    expect(html).toContain("h-6.5 w-14");
    const more = html.indexOf('aria-label="More runs at once"');
    expect(more).toBeGreaterThan(html.indexOf('role="spinbutton"'));
    expect(html.indexOf('aria-label="Fewer runs at once"')).toBeGreaterThan(more);
  });

  it("adds 1 with + or Arrow Up and takes 1 with − or Arrow Down, saving nothing", () => {
    const fetchSpy = mock(() => Promise.resolve(new Response("{}")));
    globalThis.fetch = fetchSpy as unknown as typeof fetch;
    const onChange = mock((_n: number) => {});
    const s = stepper(3, onChange);
    s.more.onStep();
    s.fewer.onStep();
    expect([s.key("ArrowUp"), s.key("ArrowDown"), s.key("Enter")]).toEqual([true, true, false]);
    expect(onChange.mock.calls.map(([n]) => n)).toEqual([4, 2, 4, 2]);
    expect(fetchSpy).not.toHaveBeenCalled();
  });

  it("makes − inactive at 1 and + inactive at 15", () => {
    const onChange = mock((_n: number) => {});
    const floor = stepper(1, onChange);
    expect([floor.more.live, floor.fewer.live]).toEqual([true, false]);
    floor.fewer.onStep();
    floor.key("ArrowDown");
    const cap = stepper(15, onChange);
    expect([cap.more.live, cap.fewer.live]).toEqual([false, true]);
    cap.more.onStep();
    cap.key("ArrowUp");
    expect(onChange).not.toHaveBeenCalled();
    const html = draw(view(2, { n: 2 }), 1);
    expect(html).toMatch(/aria-label="Fewer runs at once" disabled=""/);
    expect(html).not.toMatch(/aria-label="More runs at once" disabled=""/);
  });

  it("steps nothing while the page saves", () => {
    const onChange = mock((_n: number) => {});
    const s = stepper(3, onChange, true);
    s.more.onStep();
    s.key("ArrowDown");
    expect(onChange).not.toHaveBeenCalled();
    const html = draw(view(2, { n: 2 }), 3, true);
    expect(html).toContain('aria-disabled="true"');
    expect(html).toMatch(/aria-label="More runs at once" disabled=""/);
    expect(html).toMatch(/aria-label="Fewer runs at once" disabled=""/);
  });

  it("leaves the halves out of the tab order, which reaches the value", () => {
    const html = draw(view(2, { n: 2 }), 3);
    expect(html).toMatch(/role="spinbutton" tabindex="0"/);
    expect(html.match(/tabindex="-1" aria-label="(More|Fewer) runs at once"/g)).toHaveLength(2);
  });
});

describe("the words under the row", () => {
  it("say what N is, then one recommendation note from the saved N", () => {
    const html = draw(view(2, { n: 3, basis: "rise", stable: 5 }), 4);
    expect(html).toContain("The most runs in flight at once, across every project.");
    expect(html).toContain(
      "Recommended: 3, because 5 runs in a row at 2 finished within 1.25x the solo median, so one more can run at once.",
    );
  });

  it("say when the recommendation could not be read and keep the stepper", () => {
    const html = draw(view(2, null), 2);
    expect(html).toContain("The recommendation could not be read.");
    expect(html).toContain('aria-valuenow="2"');
  });

  it("leave no field, presets, USE pill or range error", () => {
    const html = draw(view(2, { n: 3, basis: "rise", stable: 5 }), 2);
    expect(html).not.toContain("<input");
    expect(html).not.toContain("Fill in");
    expect(html).not.toContain("USE ");
    expect(html).not.toContain("Enter a whole number");
  });
});

describe("statusWords", () => {
  const idle = { saving: false, changed: false, savedRunsAtOnce: null };
  it("says each drawn status", () => {
    expect(statusWords(idle)).toBe("No changes to save.");
    expect(statusWords({ ...idle, changed: true })).toBe("Change not saved yet.");
    expect(statusWords({ ...idle, changed: true, saving: true })).toBe("Saving…");
    expect(statusWords({ ...idle, savedRunsAtOnce: 3 })).toBe("Saved. Runs at once is now 3.");
  });
});

describe("restage", () => {
  it("stages a new N and drops it again once N is back at the saved one", () => {
    const stepped = restage({}, "runsAtOnce", 4, 3);
    expect(stepped).toEqual({ runsAtOnce: 4 });
    expect(isChanged(stepped)).toBe(true);
    const back = restage(stepped, "runsAtOnce", 3, 3);
    expect(back).toEqual({});
    expect(isChanged(back)).toBe(false);
  });

  it("keeps what else is staged", () => {
    expect(restage({ autoMerge: true }, "runsAtOnce", 3, 3)).toEqual({ autoMerge: true });
    expect(isChanged({ avoidPeak: false })).toBe(true);
  });
});
