import { describe, expect, test } from "bun:test";
import { outcome } from "./report";

describe("outcome", () => {
  test("fails the run for one route read with no fixture", () => {
    expect(outcome(new Map([["/api/peak", 1]]), new Map(), [], [])).toEqual({
      failures: ["shotsFailed reason=missing-fixture route=/api/peak count=1"],
      exitCode: 1,
    });
  });

  test("counts every read of a missing fixture with one fixed key", () => {
    expect(outcome(new Map([["/api/autonomy", 3]]), new Map(), [], [])).toEqual({
      failures: ["shotsFailed reason=missing-fixture route=/api/autonomy count=3"],
      exitCode: 1,
    });
  });

  test("fails the run for each request the fixture API does not serve", () => {
    expect(outcome(new Map(), new Map([["POST /api/projects/add", 1]]), [], [])).toEqual({
      failures: ['shotsFailed reason=unexpected-write request="POST /api/projects/add" count=1'],
      exitCode: 1,
    });
  });

  test("fails the run for each refused shot, with its width and reason", () => {
    expect(
      outcome(
        new Map(),
        new Map(),
        [{ route: "runs-runId", width: "s", detail: "the page scrolls sideways" }],
        [],
      ),
    ).toEqual({
      failures: ['shotsFailed reason=refused shot=runs-runId-s detail="the page scrolls sideways"'],
      exitCode: 1,
    });
  });

  test("fails the run for a route whose page logged an error", () => {
    expect(
      outcome(
        new Map(),
        new Map(),
        [],
        [
          {
            route: "/stats",
            detail: "comparisonNotRead the endpoint answered a document of another shape",
          },
        ],
      ),
    ).toEqual({
      failures: [
        'shotsFailed reason=page-error route=/stats detail="comparisonNotRead the endpoint answered a document of another shape"',
      ],
      exitCode: 1,
    });
  });

  test("passes when every shot was written and every read answered", () => {
    expect(outcome(new Map(), new Map(), [], [])).toEqual({ failures: [], exitCode: 0 });
  });

  test("fails the run for a quoted string no page showed, naming its state", () => {
    expect(
      outcome(new Map(), new Map(), [], [], {
        missingStrings: [{ state: "/stats@s", string: 'Runs "all" projects' }],
        overflows: [],
      }),
    ).toEqual({
      failures: [
        'shotsFailed reason=missing-string state=/stats@s string="Runs \\"all\\" projects"',
      ],
      exitCode: 1,
    });
  });

  test("fails the run for an element wider than its box, naming both widths", () => {
    expect(
      outcome(new Map(), new Map(), [], [], {
        missingStrings: [],
        overflows: [
          {
            route: "/stats",
            width: "xs",
            element: "div.card-frame",
            scrollWidth: 512,
            clientWidth: 470,
          },
        ],
      }),
    ).toEqual({
      failures: [
        'shotsFailed reason=overflow state=/stats@xs element="div.card-frame" scrollWidth=512 clientWidth=470',
      ],
      exitCode: 1,
    });
  });

  test("passes a run whose checks found nothing", () => {
    expect(
      outcome(new Map(), new Map(), [], [], { missingStrings: [], overflows: [] }).exitCode,
    ).toBe(0);
  });
});
