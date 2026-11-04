import { describe, expect, it } from "bun:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describeRunSuccess, isRunSuccess, type RunSuccess } from "./run-success";
import { RunSuccessCard } from "./run-success-card";

// success is the endpoint's answer with 20 or more runs in each window; a case overrides what it varies.
const success = (over: Partial<RunSuccess> = {}): RunSuccess => ({
  recent: { passed: 55, runs: 64, rate: 55 / 64 },
  previous: { passed: 53, runs: 58, rate: 53 / 58 },
  failure: { class: "agent-timeout", count: 4 },
  ...over,
});

// recentOf is a recent window of that many runs passed out of runs, with the rate the endpoint serves.
const recentOf = (passed: number, runs: number) => ({ passed, runs, rate: passed / runs });

describe("describeRunSuccess", () => {
  describe("the target", () => {
    it("is met at exactly 90 percent", () => {
      const view = describeRunSuccess(success({ recent: recentOf(18, 20) }));
      expect(view.state).toBe("on");
      expect(view.word).toBe("On target");
      expect(view.figure).toBe("90%");
    });

    it("is missed at 89.95 percent, and the figure reads 89%, not 90%", () => {
      const view = describeRunSuccess(success({ recent: recentOf(1799, 2000) }));
      expect(view.state).toBe("below");
      expect(view.figure).toBe("89%");
    });

    it("is missed at 89 percent", () => {
      const view = describeRunSuccess(success({ recent: recentOf(89, 100) }));
      expect(view.word).toBe("Below target");
      expect(view.figure).toBe("89%");
    });
  });

  it("prints the figure rounded down", () => {
    expect(describeRunSuccess(success()).figure).toBe("85%");
  });

  it("words the rate line and the target beside a figure", () => {
    const view = describeRunSuccess(success());
    expect(view.rate).toBe("Over the trailing 4 weeks (55 of 64 runs)");
    expect(view.target).toBe("target 90%");
  });

  describe("under 20 runs in the trailing window", () => {
    const few = success({ recent: { passed: 6, runs: 7, rate: null } });

    it("states no figure and no target", () => {
      const view = describeRunSuccess(few);
      expect(view.state).toBe("few");
      expect(view.word).toBe("Too few runs");
      expect(view.figure).toBeNull();
      expect(view.target).toBeNull();
    });

    it("says how many runs it has and how many it needs, with no detail lines", () => {
      const view = describeRunSuccess(few);
      expect(view.rate).toBe("Only 7 runs in the trailing 4 weeks; 20 needed for a rate");
      expect(view.details).toEqual([]);
    });

    it("reads run in the singular for a count of one", () => {
      const view = describeRunSuccess(success({ recent: { passed: 1, runs: 1, rate: null } }));
      expect(view.rate).toBe("Only 1 run in the trailing 4 weeks; 20 needed for a rate");
    });

    it("reads runs in the plural for a count of zero", () => {
      const view = describeRunSuccess(success({ recent: { passed: 0, runs: 0, rate: null } }));
      expect(view.rate).toBe("Only 0 runs in the trailing 4 weeks; 20 needed for a rate");
    });
  });

  describe("the previous window", () => {
    it("prints its percentage rounded down", () => {
      const view = describeRunSuccess(success({ previous: recentOf(55, 64) }));
      expect(view.details[0]).toBe("Previous 4 weeks: 85% (55 of 64 runs)");
    });

    it("says it holds too few runs when it has some but not enough", () => {
      const view = describeRunSuccess(success({ previous: { passed: 9, runs: 11, rate: null } }));
      expect(view.details[0]).toBe("Previous 4 weeks: too few runs (9 of 11 runs)");
    });

    it("says it holds no runs at zero", () => {
      const view = describeRunSuccess(success({ previous: { passed: 0, runs: 0, rate: null } }));
      expect(view.details[0]).toBe("Previous 4 weeks: no runs");
    });
  });

  describe("the top stop", () => {
    it("names the class in words and counts it against the failed runs", () => {
      expect(describeRunSuccess(success()).details[1]).toBe(
        "Top stop: agent timeout, 4 of 9 failures",
      );
    });

    it("turns every hyphen of the class into a space", () => {
      const view = describeRunSuccess(success({ failure: { class: "no-pr-opened", count: 2 } }));
      expect(view.details[1]).toBe("Top stop: no pr opened, 2 of 9 failures");
    });

    it("reads failure in the singular for one failed run", () => {
      const view = describeRunSuccess(
        success({
          recent: recentOf(63, 64),
          failure: { class: "abandoned", count: 1 },
        }),
      );
      expect(view.details[1]).toBe("Top stop: abandoned, 1 of 1 failure");
    });

    it("is absent when no run failed", () => {
      const view = describeRunSuccess(success({ recent: recentOf(64, 64), failure: undefined }));
      expect(view.details).toEqual(["Previous 4 weeks: 91% (53 of 58 runs)"]);
    });
  });
});

describe("isRunSuccess", () => {
  it("accepts a body with a failure", () => {
    expect(isRunSuccess(success())).toBe(true);
  });

  it("accepts a body the endpoint sends without a failure", () => {
    const { failure: _failure, ...body } = success();
    expect(isRunSuccess(body)).toBe(true);
  });

  it("accepts a window whose rate is null", () => {
    expect(isRunSuccess(success({ recent: { passed: 6, runs: 7, rate: null } }))).toBe(true);
  });

  it("rejects a body without a previous window", () => {
    const { previous: _previous, ...body } = success();
    expect(isRunSuccess(body)).toBe(false);
  });

  it("rejects a window whose count is not a number", () => {
    expect(isRunSuccess({ ...success(), recent: { passed: "55", runs: 64, rate: 0.5 } })).toBe(
      false,
    );
  });

  describe("a window", () => {
    const rejects = (recent: unknown) => isRunSuccess({ ...success(), recent });

    it("rejects a negative count", () => {
      expect(rejects({ passed: -1, runs: 64, rate: 0.5 })).toBe(false);
    });

    it("rejects a fractional count", () => {
      expect(rejects({ passed: 55.5, runs: 64, rate: 0.5 })).toBe(false);
    });

    it("rejects more passed than counted", () => {
      expect(rejects({ passed: 65, runs: 64, rate: 0.5 })).toBe(false);
    });

    it("rejects a count that is not finite", () => {
      expect(rejects({ passed: Number.NaN, runs: 64, rate: 0.5 })).toBe(false);
      expect(rejects({ passed: 55, runs: Number.POSITIVE_INFINITY, rate: 0.5 })).toBe(false);
    });

    it("rejects a rate over no runs", () => {
      expect(rejects({ passed: 0, runs: 0, rate: 0.5 })).toBe(false);
    });

    it("rejects a rate outside 0 to 1", () => {
      expect(rejects({ passed: 55, runs: 64, rate: 1.5 })).toBe(false);
      expect(rejects({ passed: 55, runs: 64, rate: -0.1 })).toBe(false);
      expect(rejects({ passed: 55, runs: 64, rate: Number.NaN })).toBe(false);
    });
  });

  it("rejects a failure of another shape", () => {
    expect(isRunSuccess({ ...success(), failure: { class: "agent-timeout" } })).toBe(false);
  });

  it("rejects what is not an object", () => {
    expect(isRunSuccess(null)).toBe(false);
  });
});

describe("RunSuccessCard while it reads", () => {
  const loading = (projectChosen: boolean) =>
    renderToStaticMarkup(
      createElement(RunSuccessCard, { project: projectChosen ? "acme/widgets" : null }),
    );

  it("names the card, defines it and says it is reading", () => {
    const html = loading(false);
    expect(html).toContain("run success</h2>");
    expect(html).toContain(
      "Run success is the share of runs that end in a PR with passing checks.",
    );
    expect(html).toContain('aria-busy="true"');
    expect(html).toContain('role="status"');
    expect(html).toContain("Reading the run success…");
  });

  it("holds the ready card's body unseen and out of the reading order", () => {
    expect(loading(false)).toContain('aria-hidden="true" class="invisible');
  });

  it("holds the same body with a project chosen as without one", () => {
    const body = (html: string) =>
      html.slice(0, html.indexOf('aria-labelledby="run-history-title"'));
    expect(loading(true)).not.toContain("all projects");
    expect(body(loading(true))).toBe(body(loading(false)));
  });
});
