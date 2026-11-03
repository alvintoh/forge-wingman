import { afterEach, describe, expect, it, mock } from "bun:test";
import { isValidElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { RUNS_AT_ONCE_PRESETS, type Recommendation, type RunsAtOnceView } from "./runs-at-once";
import { Presets, Recommended, RunsAtOnceSection } from "./runs-at-once-section";
import { runsAtOnceChange, statusWords } from "./save-state";

const RULE = { stable_runs: 5, rise_within: 1.25, halve_beyond: 1.5 };

function view(saved: number, r: Partial<Recommendation> | null, cap = 20): RunsAtOnceView {
  return {
    runs_at_once: saved,
    platform_cap: cap,
    recommendation: r === null ? null : { n: saved, basis: "steady", stable: 3, ...RULE, ...r },
  };
}

function draw(v: RunsAtOnceView, text: string, invalid = false): string {
  return renderToStaticMarkup(
    <RunsAtOnceSection
      read={{ status: "ready", body: v, url: "/api/runs-at-once" }}
      view={v}
      text={text}
      invalid={invalid}
      disabled={false}
      onFill={() => {}}
    />,
  );
}

// pills is every pill a tree holds, by its label, with the props it was given.
function pills(node: ReactNode): { label: string; onClick: () => void; pressed: unknown }[] {
  if (Array.isArray(node)) {
    return node.flatMap(pills);
  }
  if (!isValidElement<Record<string, unknown>>(node)) {
    return [];
  }
  const { props } = node;
  const own =
    typeof props.label === "string" && typeof props.onClick === "function"
      ? [
          {
            label: props.label,
            onClick: props.onClick as () => void,
            pressed: props["aria-pressed"],
          },
        ]
      : [];
  return [...own, ...pills(props.children as ReactNode)];
}

const realFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = realFetch;
});

describe("USE R", () => {
  it("shows while R differs from the number in the field", () => {
    const html = draw(view(2, { n: 3, basis: "rise", stable: 5 }), "2");
    expect(html).toContain("Recommended: 3");
    expect(html).toContain("USE 3");
    expect(html).toContain(
      "5 runs in a row at 2 finished within 1.25x the solo median, so one more can run at once.",
    );
  });

  it("is not shown while R equals the number in the field", () => {
    expect(draw(view(2, { n: 2 }), "2")).not.toContain("USE ");
    expect(draw(view(2, { n: 1, basis: "slow" }), "1")).not.toContain("USE ");
  });

  it("shows while the field holds no N", () => {
    expect(draw(view(2, { n: 3, basis: "rise", stable: 5 }), "25", true)).toContain("USE 3");
  });

  it("fills the field with R and saves nothing", () => {
    const fetchSpy = mock(() => Promise.resolve(new Response("{}")));
    globalThis.fetch = fetchSpy as unknown as typeof fetch;
    const onFill = mock((_text: string) => {});
    const tree = Recommended({
      view: view(2, { n: 1, basis: "slow" }),
      field: 2,
      disabled: false,
      onFill,
    });
    const use = pills(tree).find((p) => p.label === "USE 1");
    use?.onClick();
    expect(onFill).toHaveBeenCalledWith("1");
    expect(fetchSpy).not.toHaveBeenCalled();
  });
});

describe("the presets", () => {
  it("offer every preset the cap allows, none chosen while the field holds another number", () => {
    const html = draw(view(2, { n: 2 }), "2");
    for (const preset of RUNS_AT_ONCE_PRESETS) {
      expect(html).toContain(`aria-label="Fill in ${preset}"`);
    }
    expect(html).not.toContain('aria-pressed="true"');
  });

  it("show the one equal to the field's number chosen, pressed with a tick", () => {
    const tree = Presets({ platformCap: 20, field: 10, disabled: false, onFill: () => {} });
    expect(pills(tree).map((p) => [p.label, p.pressed])).toEqual([
      ["5", false],
      ["10", true],
      ["15", false],
    ]);
    const html = draw(view(2, { n: 3, basis: "rise", stable: 5 }), "10");
    const chosen = html.match(/<button[^>]*aria-pressed="true"[^>]*>(.*?)<\/button>/)?.[1] ?? "";
    expect(chosen).toContain("<svg");
    expect(chosen).toContain(">10<");
  });

  it("leave out a preset above the platform cap", () => {
    const html = draw(view(2, { n: 2 }, 12), "2");
    expect(html).toContain('aria-label="Fill in 10"');
    expect(html).not.toContain('aria-label="Fill in 15"');
  });

  it("fill the field and save nothing", () => {
    const fetchSpy = mock(() => Promise.resolve(new Response("{}")));
    globalThis.fetch = fetchSpy as unknown as typeof fetch;
    const onFill = mock((_text: string) => {});
    for (const p of pills(Presets({ platformCap: 20, field: 2, disabled: false, onFill }))) {
      p.onClick();
    }
    expect(onFill.mock.calls.map(([text]) => text)).toEqual(RUNS_AT_ONCE_PRESETS.map(String));
    expect(fetchSpy).not.toHaveBeenCalled();
  });
});

describe("the field", () => {
  it("marks a number outside the range and says the range", () => {
    const html = draw(view(2, { n: 2 }), "25", true);
    expect(html).toContain('aria-invalid="true"');
    expect(html).toContain("border-stop");
    expect(html).toContain("Enter a whole number from 1 to 20.");
    expect(html).toContain(
      "The most runs in flight at once, across every project: a whole number from 1 to 20.",
    );
  });

  it("says when the recommendation could not be read and keeps the field", () => {
    const html = draw(view(2, null), "2");
    expect(html).toContain("The recommendation could not be read.");
    expect(html).toContain('value="2"');
  });
});

describe("runsAtOnceChange", () => {
  const saved = view(2, { n: 2 });
  it("is no change while the text reads as the saved N", () => {
    expect(runsAtOnceChange(undefined, saved)).toEqual({ changed: false, invalid: false });
    expect(runsAtOnceChange(" 2", saved)).toEqual({ changed: false, invalid: false });
  });

  it("is a change for another N and invalid for no N", () => {
    expect(runsAtOnceChange("3", saved)).toEqual({ changed: true, invalid: false });
    expect(runsAtOnceChange("21", saved)).toEqual({ changed: true, invalid: true });
    expect(runsAtOnceChange("", saved)).toEqual({ changed: true, invalid: true });
  });
});

describe("statusWords", () => {
  const idle = { saving: false, invalid: false, changed: false, savedRunsAtOnce: null };
  it("says each drawn status", () => {
    expect(statusWords(idle)).toBe("No changes to save.");
    expect(statusWords({ ...idle, changed: true })).toBe("Change not saved yet.");
    expect(statusWords({ ...idle, changed: true, invalid: true })).toBe("Fix the number to save.");
    expect(statusWords({ ...idle, changed: true, saving: true })).toBe("Saving…");
    expect(statusWords({ ...idle, savedRunsAtOnce: 3 })).toBe("Saved. Runs at once is now 3.");
  });
});
