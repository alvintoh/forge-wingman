import { describe, expect, it } from "bun:test";
import { elementsOf, press } from "./element-tree";
import { RadioChoice } from "./filter-radio";
import { arrowStep, wrapIndex } from "./radio-keys";

function noop() {}

function radio(arrows?: { tabStop: boolean }) {
  const button = elementsOf(
    <RadioChoice labelledBy="x" checked={false} onChoose={noop} bulletClass="" arrows={arrows} />,
  ).find((element) => element.props.role === "radio");
  if (button === undefined) {
    throw new Error("no radio drawn");
  }
  return button;
}

describe("arrowStep", () => {
  it("steps forward on down and right, back on up and left, and not on any other key", () => {
    expect(["ArrowDown", "ArrowRight"].map(arrowStep)).toEqual([1, 1]);
    expect(["ArrowUp", "ArrowLeft"].map(arrowStep)).toEqual([-1, -1]);
    expect(["Tab", "Enter", " ", "a"].map(arrowStep)).toEqual([0, 0, 0, 0]);
  });
});

describe("wrapIndex", () => {
  it("wraps one step past either end of the list", () => {
    expect(wrapIndex(3, 3)).toBe(0);
    expect(wrapIndex(-1, 3)).toBe(2);
    expect(wrapIndex(1, 3)).toBe(1);
  });
});

describe("a radio that opts in to arrows", () => {
  it("is a tab stop only when the group names it so", () => {
    expect(radio({ tabStop: true }).props.tabIndex).toBe(0);
    expect(radio({ tabStop: false }).props.tabIndex).toBe(-1);
  });
});

describe("a radio with no arrows, as the project filter's", () => {
  it("keeps its own tab stop and takes no keys", () => {
    const button = radio();
    expect(button.props.tabIndex).toBeUndefined();
    expect(button.props.onKeyDown).toBeUndefined();
  });
});

// A radio group as the browser holds one: buttons that record the focus and the click they get.
function group(count: number) {
  const log: string[] = [];
  const buttons = Array.from({ length: count }, (_, at) => ({
    focus: () => log.push(`focus ${at}`),
    click: () => log.push(`click ${at}`),
    closest: () => ({ querySelectorAll: () => buttons }),
  }));
  // key presses `name` on the radio at `from` and counts the times the page was kept from scrolling.
  function key(from: number, name: string): number {
    let prevented = 0;
    press(radio({ tabStop: true }), "onKeyDown", {
      key: name,
      preventDefault: () => (prevented += 1),
      currentTarget: buttons[from],
    });
    return prevented;
  }
  return { log, key };
}

describe("a radio's arrow keys", () => {
  it("focus and check the next radio, wrapping from the last to the first", () => {
    const { log, key } = group(3);
    key(2, "ArrowDown");
    expect(log).toEqual(["focus 0", "click 0"]);
  });

  it("focus and check the previous radio, wrapping from the first to the last", () => {
    const { log, key } = group(3);
    key(0, "ArrowUp");
    expect(log).toEqual(["focus 2", "click 2"]);
  });

  it("move to the neighbour inside the list", () => {
    const { log, key } = group(3);
    key(0, "ArrowRight");
    key(2, "ArrowLeft");
    expect(log).toEqual(["focus 1", "click 1", "focus 1", "click 1"]);
  });

  it("hold the page still while they move the check, and leave other keys alone", () => {
    const { log, key } = group(2);
    expect(key(0, "ArrowRight")).toBe(1);
    expect(key(0, "Tab")).toBe(0);
    expect(log).toEqual(["focus 1", "click 1"]);
  });
});
