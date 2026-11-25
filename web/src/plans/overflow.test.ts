import { describe, expect, it } from "bun:test";
import { columnWidth, cueWords, plansToScroll, scrolls, tableWidth } from "./overflow";
import type { Measure } from "./overflow";

// Five plans in a region 934px wide, beside a 176px label (11rem at a 16px root). The cue's 48px
// gutter and the label leave 710px, which holds three plans of 232px, so they stretch to 236.67px.
const FIVE: Measure = { scrollLeft: 0, clientWidth: 934, label: 176, plans: 5 };
const END = 473;

describe("scrolls", () => {
  it("is false while every plan at its least width fits beside the label", () => {
    expect(scrolls({ ...FIVE, plans: 3 })).toBe(false);
    expect(scrolls({ ...FIVE, plans: 2, clientWidth: 700 })).toBe(false);
  });

  it("is true once they do not, and still true at the end of the scroll", () => {
    expect(scrolls({ ...FIVE, plans: 4 })).toBe(true);
    expect(scrolls(FIVE)).toBe(true);
    expect(scrolls({ ...FIVE, scrollLeft: END })).toBe(true);
  });

  it("ignores a pixel of rounding", () => {
    // Four plans are 176 + 4 * 232 = 1104px wide.
    expect(scrolls({ ...FIVE, plans: 4, clientWidth: 1103 })).toBe(false);
    expect(scrolls({ ...FIVE, plans: 4, clientWidth: 1102 })).toBe(true);
  });

  it("is false where there is nothing to measure", () => {
    expect(scrolls({ scrollLeft: 0, clientWidth: 0, label: 0, plans: 3 })).toBe(false);
    expect(scrolls({ ...FIVE, plans: 0 })).toBe(false);
  });
});

describe("columnWidth", () => {
  it("stretches the whole columns that fit to fill the room beside the label and the gutter", () => {
    expect(columnWidth(FIVE)).toBeCloseTo(236.67, 2);
  });

  it("is the least width where the room holds a whole number of them", () => {
    expect(columnWidth({ ...FIVE, clientWidth: 176 + 48 + 3 * 232 })).toBe(232);
  });

  it("counts a column within a pixel of the room as whole", () => {
    expect(columnWidth({ ...FIVE, clientWidth: 176 + 48 + 3 * 232 - 0.5 })).toBe(232);
  });

  it("is never under the least width, even where not one column fits", () => {
    expect(columnWidth({ ...FIVE, clientWidth: 400 })).toBe(232);
  });
});

describe("tableWidth", () => {
  it("is the label and every plan at its column width", () => {
    expect(tableWidth(FIVE)).toBeCloseTo(176 + 5 * 236.67, 1);
  });
});

describe("plansToScroll", () => {
  it("is 0 when every plan fits", () => {
    expect(plansToScroll({ ...FIVE, plans: 3 })).toBe(0);
    expect(plansToScroll({ ...FIVE, plans: 2, clientWidth: 700 })).toBe(0);
  });

  it("counts the plans past the room the plans have, none of them cut at rest", () => {
    expect(plansToScroll(FIVE)).toBe(2);
    expect(plansToScroll({ ...FIVE, plans: 4 })).toBe(1);
  });

  it("counts every plan when none fits beside the label", () => {
    expect(plansToScroll({ ...FIVE, clientWidth: 400 })).toBe(5);
  });

  it("falls as the table scrolls right, and is 0 at its end", () => {
    expect(plansToScroll({ ...FIVE, scrollLeft: 237 })).toBe(1);
    expect(plansToScroll({ ...FIVE, scrollLeft: END })).toBe(0);
  });

  it("holds a plan within a pixel of the edge as whole, and one 2px short as cut off", () => {
    // Four plans of 232px, three of them whole in the 696px of room.
    const four: Measure = { scrollLeft: 0, clientWidth: 176 + 48 + 696, label: 176, plans: 4 };
    expect(plansToScroll(four)).toBe(1);
    expect(plansToScroll({ ...four, scrollLeft: 231 })).toBe(0);
    expect(plansToScroll({ ...four, scrollLeft: 230 })).toBe(1);
  });

  it("scales with the label, so a smaller root font size changes nothing", () => {
    expect(plansToScroll({ scrollLeft: 0, clientWidth: 467, label: 88, plans: 5 })).toBe(2);
  });

  it("is 0 where there is nothing to measure", () => {
    expect(plansToScroll({ scrollLeft: 0, clientWidth: 0, label: 0, plans: 3 })).toBe(0);
    expect(plansToScroll({ ...FIVE, plans: 0 })).toBe(0);
  });
});

describe("cueWords", () => {
  it("says how many plans, in the plural but for one", () => {
    expect(cueWords(2)).toBe("+2 plans");
    expect(cueWords(5)).toBe("+5 plans");
    expect(cueWords(1)).toBe("+1 plan");
  });
});
