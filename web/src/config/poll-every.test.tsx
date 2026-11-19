import { describe, expect, it, mock } from "bun:test";
import { isValidElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { PollEvery } from "./poll-every";

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

function draw(value: number | null, disabled = false): string {
  return renderToStaticMarkup(
    <PollEvery labelledBy="poll" value={value} disabled={disabled} onChoose={() => {}} />,
  );
}

type Group = { querySelector: (selector: string) => { focus: () => void } | null };
type Target = { dataset: Record<string, string>; parentElement: Group | null };

type Handlers = {
  onClick: (event: { currentTarget: Target }) => void;
  onKeyDown: (event: { key: string; currentTarget: Target; preventDefault: () => void }) => void;
};

// segment is the 10 or 15 minute segment's handlers, from the control as React builds it.
function segment(
  minutes: number,
  onChoose: (m: number) => void,
  group: Group | null = null,
  value: number | null = 15,
) {
  const tree = PollEvery({ labelledBy: "poll", value, disabled: false, onChoose });
  const [props] = propsOf(tree, "minutes", minutes);
  const handlers = props as unknown as Handlers;
  const target: Target = { dataset: { minutes: `${minutes}` }, parentElement: group };
  return {
    click: () => handlers.onClick({ currentTarget: target }),
    // key presses key on the segment and says whether it kept the page from scrolling.
    key: (key: string) => {
      let prevented = false;
      handlers.onKeyDown({
        key,
        currentTarget: target,
        preventDefault: () => {
          prevented = true;
        },
      });
      return prevented;
    },
  };
}

// checked is each segment's label and aria-checked, in order.
function checked(html: string): string[] {
  return [...html.matchAll(/aria-checked="(true|false)"[\s\S]*?(\d+ minutes)</g)].map(
    ([, on, label]) => `${label}:${on}`,
  );
}

describe("the Poll every control", () => {
  it("offers 10 and 15 minutes as a radio group with the saved value ticked", () => {
    const html = draw(10);
    expect(html).toContain('role="radiogroup" aria-labelledby="poll" aria-disabled="false"');
    expect(checked(html)).toEqual(["10 minutes:true", "15 minutes:false"]);
    expect(html.match(/role="radio"/g)).toHaveLength(2);
  });

  it("marks the ticked segment with a check, not colour alone", () => {
    const html = draw(15);
    expect(html.match(/<svg/g)).toHaveLength(1);
    expect(html.indexOf("<svg")).toBeGreaterThan(html.indexOf('data-minutes="15"'));
    expect(html.indexOf("<svg")).toBeLessThan(html.indexOf(">15 minutes<"));
  });

  it("gives each segment a 44 high, 106 wide target", () => {
    const html = draw(15);
    expect(html.match(/role="radio"[^>]*class="[^"]*h-11 w-26\.5/g)).toHaveLength(2);
  });

  it("keeps one tab stop, on the ticked segment", () => {
    const html = draw(15);
    expect(html).toMatch(/data-minutes="10" tabindex="-1"/);
    expect(html).toMatch(/data-minutes="15" tabindex="0"/);
  });

  it("is inactive with neither segment ticked while the value is unread", () => {
    const html = draw(null);
    expect(html).toContain('aria-disabled="true"');
    expect(checked(html)).toEqual(["10 minutes:false", "15 minutes:false"]);
    expect(html.match(/disabled=""/g)).toHaveLength(2);
    expect(html).not.toContain("<svg");
  });

  it("is inactive with the staged segment still ticked while the page saves", () => {
    const html = draw(10, true);
    expect(html).toContain('aria-disabled="true"');
    expect(checked(html)).toEqual(["10 minutes:true", "15 minutes:false"]);
    expect(html.match(/disabled=""/g)).toHaveLength(2);
    expect(html).toContain("<svg");
    expect(html).not.toContain("bg-accent");
  });

  it("chooses a segment on a tap and the other one on an arrow key", () => {
    const onChoose = mock((_m: number) => {});
    segment(10, onChoose).click();
    const fifteen = segment(15, onChoose);
    expect([fifteen.key("ArrowLeft"), fifteen.key("ArrowRight"), fifteen.key("Enter")]).toEqual([
      true,
      true,
      false,
    ]);
    expect(onChoose.mock.calls.map(([m]) => m)).toEqual([10, 10, 10]);
  });

  it("moves focus to the segment an arrow key chooses", () => {
    const focus = mock(() => {});
    const querySelector = mock((_selector: string) => ({ focus }));
    segment(15, () => {}, { querySelector }).key("ArrowRight");
    expect(querySelector.mock.calls).toEqual([['[data-minutes="10"]']]);
    expect(focus).toHaveBeenCalledTimes(1);
  });

  it("puts the tab stop on the first segment when the value is not a choice", () => {
    const html = draw(12);
    expect(checked(html)).toEqual(["10 minutes:false", "15 minutes:false"]);
    expect(html).toMatch(/data-minutes="10" tabindex="0"/);
    expect(html).toMatch(/data-minutes="15" tabindex="-1"/);
  });
});
