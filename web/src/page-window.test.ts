import { describe, expect, it } from "bun:test";
import { pageWindow } from "./page-window";

describe("pageWindow", () => {
  it("splits 37 rows into four pages of ten, the last holding rows 31 to 37", () => {
    expect(pageWindow(37, 0, 10)).toMatchObject({ last: 3, from: 1, to: 10, paged: true });
    expect(pageWindow(37, 3, 10)).toMatchObject({ page: 3, from: 31, to: 37, next: false });
  });

  it("splits 37 rows into eight pages of five", () => {
    expect(pageWindow(37, 0, 5)).toMatchObject({ last: 7, from: 1, to: 5 });
    expect(pageWindow(37, 7, 5)).toMatchObject({ from: 36, to: 37 });
  });

  it("clamps a page kept across a wider page size to the last one", () => {
    expect(pageWindow(37, 7, 10).page).toBe(3);
  });

  it("allows PREVIOUS and NEXT only where a page lies that way", () => {
    expect(pageWindow(37, 0, 10)).toMatchObject({ previous: false, next: true });
    expect(pageWindow(37, 1, 10)).toMatchObject({ previous: true, next: true });
  });

  it("draws no pager for rows that fit one page", () => {
    expect(pageWindow(10, 0, 10).paged).toBe(false);
    expect(pageWindow(11, 0, 10).paged).toBe(true);
  });
});
