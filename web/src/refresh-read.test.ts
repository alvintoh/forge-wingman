import { describe, expect, it } from "bun:test";
import { buttonLabel, failedWords, returnDue, updatedWords } from "./refresh-read";

const READ_AT = 1_000_000;
const MINUTE = 60_000;

describe("returnDue", () => {
  it("waits until the read on screen is 30 seconds old", () => {
    expect(returnDue(READ_AT, 0, READ_AT + 29_999)).toBe(false);
    expect(returnDue(READ_AT, 0, READ_AT + 30_000)).toBe(true);
  });

  it("counts from the last ask when that is newer than the read", () => {
    expect(returnDue(READ_AT, READ_AT + 20_000, READ_AT + 49_999)).toBe(false);
    expect(returnDue(READ_AT, READ_AT + 20_000, READ_AT + 50_000)).toBe(true);
  });

  it("does not read before the first read has landed", () => {
    expect(returnDue(0, 0, 10 * MINUTE)).toBe(false);
  });
});

describe("buttonLabel", () => {
  it("names the button for each of its two states", () => {
    expect(buttonLabel(false)).toBe("Read the page again");
    expect(buttonLabel(true)).toBe("Reading the page again");
  });
});

describe("the age words", () => {
  it("say how long ago the page read", () => {
    expect(updatedWords(READ_AT, READ_AT)).toBe("updated just now");
    expect(updatedWords(READ_AT, READ_AT + 2 * MINUTE)).toBe("updated 2m ago");
  });

  it("say a failed re-read leaves the read on screen standing", () => {
    expect(failedWords(READ_AT, READ_AT + 6 * MINUTE)).toBe(
      "The page could not be refreshed; showing the read from 6m ago.",
    );
  });
});
