import { describe, expect, it } from "bun:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describeRunSuccess, isRunSuccess, type RunSuccess } from "./run-success";
import { RunSuccessCard } from "./run-success-card";

// success is the endpoint's answer with both windows full; a case overrides what it varies.
const success = (over: Partial<RunSuccess> = {}): RunSuccess => ({
  recent: { passed: 24, runs: 30, rate: 24 / 30 },
  previous: { passed: 27, runs: 30, rate: 27 / 30 },
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
    expect(describeRunSuccess(success({ recent: recentOf(55, 64) })).figure).toBe("85%");
  });

  it("words the rate line and the target beside a figure", () => {
    const view = describeRunSuccess(success());
    expect(view.rate).toBe("Over the last 30 runs (24 of 30)");
    expect(view.target).toBe("target 90%");
  });

  it("names the runs it counted when fewer than 30 exist", () => {
    expect(describeRunSuccess(success({ recent: recentOf(20, 25) })).rate).toBe(
      "Over the last 25 runs (20 of 25)",
    );
  });

  describe("under 20 finished runs", () => {
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
      expect(view.rate).toBe("Only 7 runs so far; 20 needed for a rate");
      expect(view.details).toEqual([]);
    });

    it("reads run in the singular for a count of one", () => {
      const view = describeRunSuccess(success({ recent: { passed: 1, runs: 1, rate: null } }));
      expect(view.rate).toBe("Only 1 run so far; 20 needed for a rate");
    });

    it("reads runs in the plural for a count of zero", () => {
      const view = describeRunSuccess(success({ recent: { passed: 0, runs: 0, rate: null } }));
      expect(view.rate).toBe("Only 0 runs so far; 20 needed for a rate");
    });
  });

  describe("the previous window", () => {
    it("words a full window with its rate and counts", () => {
      expect(describeRunSuccess(success()).details[0]).toBe("Previous 30 runs: 90% (27 of 30)");
    });

    it("prints its percentage rounded down", () => {
      const view = describeRunSuccess(success({ previous: recentOf(26, 30) }));
      expect(view.details[0]).toBe("Previous 30 runs: 86% (26 of 30)");
    });

    it("names the runs it holds when fewer than 30 came before", () => {
      const view = describeRunSuccess(success({ previous: recentOf(19, 22) }));
      expect(view.details[0]).toBe("Previous 22 runs: 86% (19 of 22)");
    });

    it("says it holds too few runs when it has some but not enough", () => {
      const view = describeRunSuccess(success({ previous: { passed: 9, runs: 11, rate: null } }));
      expect(view.details[0]).toBe("Previous 11 runs: too few for a rate (9 of 11)");
    });

    it("reads run in the singular for one run", () => {
      const view = describeRunSuccess(success({ previous: { passed: 1, runs: 1, rate: null } }));
      expect(view.details[0]).toBe("Previous 1 run: too few for a rate (1 of 1)");
    });

    it("says it holds no runs at zero", () => {
      const view = describeRunSuccess(success({ previous: { passed: 0, runs: 0, rate: null } }));
      expect(view.details[0]).toBe("Previous runs: none");
    });
  });

  describe("the top stop", () => {
    it("names the class in words and counts it against the failed runs", () => {
      expect(describeRunSuccess(success()).details[1]).toBe(
        "Top stop: agent timeout, 4 of 6 failures",
      );
    });

    it("turns every hyphen of the class into a space", () => {
      const view = describeRunSuccess(success({ failure: { class: "no-pr-opened", count: 2 } }));
      expect(view.details[1]).toBe("Top stop: no pr opened, 2 of 6 failures");
    });

    it("reads failure in the singular for one failed run", () => {
      const view = describeRunSuccess(
        success({
          recent: recentOf(29, 30),
          failure: { class: "abandoned", count: 1 },
        }),
      );
      expect(view.details[1]).toBe("Top stop: abandoned, 1 of 1 failure");
    });

    it("is absent when no run failed", () => {
      const view = describeRunSuccess(success({ recent: recentOf(30, 30), failure: undefined }));
      expect(view.details).toEqual(["Previous 30 runs: 90% (27 of 30)"]);
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
