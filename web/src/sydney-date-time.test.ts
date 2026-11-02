import { describe, expect, it } from "bun:test";
import { sydneyDateTime } from "./sydney-date-time";

describe("sydneyDateTime", () => {
  it("prints the day, month, year and 24-hour time on Sydney's clock", () => {
    expect(sydneyDateTime("2026-10-29T13:42:00Z")).toBe("30 Oct 2026, 00:42");
    expect(sydneyDateTime("2026-10-29T23:42:00Z")).toBe("30 Oct 2026, 10:42");
  });

  it("leaves the day without a leading zero and spells September as Sep", () => {
    expect(sydneyDateTime("2026-09-03T02:05:00Z")).toBe("3 Sep 2026, 12:05");
  });

  it("carries the year across a 1 January in Sydney", () => {
    expect(sydneyDateTime("2025-12-31T13:00:00Z")).toBe("1 Jan 2026, 00:00");
  });

  it("is null for a text that is no instant", () => {
    expect(sydneyDateTime("garbage")).toBeNull();
  });
});
