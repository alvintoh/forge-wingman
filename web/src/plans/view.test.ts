import { describe, expect, it } from "bun:test";
import { readFileSync } from "node:fs";
import {
  figuresState,
  figuresUrl,
  isPlansView,
  listedShapes,
  modelGroups,
  modelsOf,
  pick,
  reloadFor,
  resolveChoice,
  statusLine,
  stageLabel,
  tokensLine,
} from "./view";
import type { PlansView } from "./view";
import type { Read } from "../stats/use-read";

const fixture: unknown = JSON.parse(
  readFileSync(new URL("../../fixtures/plans.json", import.meta.url), "utf8"),
);

function priced(model: string, input = 0.1, output = 0.5) {
  return { model, input_usd_per_mtok: input, output_usd_per_mtok: output };
}

function plansView(overrides: Partial<PlansView> = {}): PlansView {
  return {
    plans: [
      { plan: "paid", models: [priced("paid/zeta"), priced("shared/alpha")] },
      { plan: "free", models: [priced("shared/alpha", 0, 0), priced("free/beta", 0, 0)] },
    ],
    shapes: [
      { stage: "plan", runs: 7, shape: { fresh: 52000, cached: 2200, output: 1500 } },
      { stage: "build", runs: 2, shape: null },
      { stage: "review", runs: 5, shape: { fresh: 13000, cached: 0, output: 1200 } },
    ],
    model: null,
    stage: null,
    ...overrides,
  };
}

describe("isPlansView", () => {
  it("accepts the endpoint's answer", () => {
    expect(isPlansView(fixture)).toBe(true);
  });

  it("rejects an answer with no shapes", () => {
    expect(isPlansView({ plans: [], model: null, stage: null })).toBe(false);
  });

  it("rejects a shape whose count is not a number", () => {
    const shapes = [{ stage: "plan", runs: 1, shape: { fresh: "52000", cached: 0, output: 0 } }];
    expect(isPlansView({ plans: [], shapes, model: null, stage: null })).toBe(false);
  });

  it("rejects a priced model with no rates", () => {
    const plans = [{ plan: "paid", models: [{ model: "paid/zeta" }] }];
    expect(isPlansView({ plans, shapes: [], model: null, stage: null })).toBe(false);
  });
});

describe("modelGroups", () => {
  it("lists the plans and their models in the server's order, a repeated model once", () => {
    expect(modelGroups(plansView())).toEqual([
      {
        plan: "paid",
        models: [
          { id: "paid/zeta", name: "zeta" },
          { id: "shared/alpha", name: "shared/alpha" },
        ],
      },
      { plan: "free", models: [{ id: "free/beta", name: "beta" }] },
    ]);
  });

  it("leaves out a plan whose models were all listed before", () => {
    const plans = [
      { plan: "a", models: [priced("a/one")] },
      { plan: "b", models: [priced("a/one")] },
    ];
    expect(modelGroups(plansView({ plans })).map((group) => group.plan)).toEqual(["a"]);
  });
});

describe("modelsOf", () => {
  it("lists every id in the order the select shows them", () => {
    expect(modelsOf(plansView())).toEqual(["paid/zeta", "shared/alpha", "free/beta"]);
  });
});

describe("listedShapes", () => {
  it("leaves out a stage with no shape and keeps the order", () => {
    expect(listedShapes(plansView()).map((listed) => listed.stage)).toEqual(["plan", "review"]);
  });
});

describe("resolveChoice", () => {
  it("chooses the first model and the first listed stage before the owner chooses", () => {
    expect(resolveChoice(plansView(), {})).toEqual({ model: "paid/zeta", stage: "plan" });
  });

  it("skips a model no plan prices above $0 for the first that one does", () => {
    const plans = [
      { plan: "free", models: [priced("free/beta", 0, 0)] },
      { plan: "paid", models: [priced("paid/zeta")] },
    ];
    expect(resolveChoice(plansView({ plans }), {})?.model).toBe("paid/zeta");
  });

  it("counts a model priced above $0 by any plan that lists it", () => {
    const plans = [
      { plan: "free", models: [priced("shared/alpha", 0, 0)] },
      { plan: "paid", models: [priced("shared/alpha")] },
    ];
    expect(resolveChoice(plansView({ plans }), {})?.model).toBe("shared/alpha");
  });

  it("chooses the first model when every one is $0", () => {
    const plans = [
      { plan: "free", models: [priced("free/beta", 0, 0), priced("free/gamma", 0, 0)] },
    ];
    expect(resolveChoice(plansView({ plans }), {})?.model).toBe("free/beta");
  });

  it("opens the endpoint's answer on a model priced above $0, whichever plan comes first", () => {
    const view: PlansView = JSON.parse(JSON.stringify(fixture));
    const rates = (id: string) =>
      view.plans.flatMap((plan) => plan.models).filter((model) => model.model === id);
    for (const plans of [view.plans, [...view.plans].reverse()]) {
      const model = resolveChoice({ ...view, plans }, {})?.model ?? "";
      expect(
        rates(model).some((rate) => rate.input_usd_per_mtok + rate.output_usd_per_mtok > 0),
      ).toBe(true);
    }
    expect(resolveChoice(view, {})?.model).toBe("opencode-go/claude-haiku-5-5");
  });

  it("is the choice the fixture's figures were priced at", () => {
    const view: PlansView = JSON.parse(JSON.stringify(fixture));
    expect(resolveChoice(view, {})).toEqual({ model: view.model, stage: view.stage });
  });

  it("keeps a model and a stage the owner chose", () => {
    expect(resolveChoice(plansView(), { model: "free/beta", stage: "review" })).toEqual({
      model: "free/beta",
      stage: "review",
    });
  });

  it("falls back when what was chosen is no longer listed", () => {
    const view = plansView();
    expect(resolveChoice(view, { model: "gone/model", stage: "build" })).toEqual({
      model: "paid/zeta",
      stage: "plan",
    });
  });

  it("chooses nothing while no stage has a shape", () => {
    const shapes = [{ stage: "plan", runs: 1, shape: null }];
    expect(resolveChoice(plansView({ shapes }), {})).toBeNull();
  });

  it("chooses nothing while no plan prices a model", () => {
    expect(resolveChoice(plansView({ plans: [] }), {})).toBeNull();
  });
});

describe("pick", () => {
  it("records a model and leaves the stage as it was", () => {
    expect(pick({ stage: "review" }, { model: "paid/zeta" })).toEqual({
      model: "paid/zeta",
      stage: "review",
    });
  });

  it("records a stage and leaves the model as it was", () => {
    expect(pick({ model: "paid/zeta" }, { stage: "review" })).toEqual({
      model: "paid/zeta",
      stage: "review",
    });
  });

  it("starts from nothing chosen", () => {
    expect(pick({}, { stage: "build" })).toEqual({ stage: "build" });
  });
});

describe("statusLine", () => {
  it("says the plans are being read while either read is out", () => {
    expect(statusLine("loading", "none")).toBe("Reading the plans…");
    expect(statusLine("ready", "loading")).toBe("Reading the plans…");
  });

  it("says they could not be read when either read failed", () => {
    expect(statusLine("error", "none")).toBe("The plans could not be read.");
    expect(statusLine("ready", "error")).toBe("The plans could not be read.");
  });

  it("is empty once both are in", () => {
    expect(statusLine("ready", "ready")).toBe("");
    expect(statusLine("ready", "none")).toBe("");
  });
});

describe("figuresUrl", () => {
  it("asks for the model and the stage together, encoded", () => {
    const expected = "/api/plans?model=paid%2Fzeta&stage=build";
    expect(figuresUrl({ model: "paid/zeta", stage: "build" })).toBe(expected);
  });

  it("asks for nothing while nothing is chosen", () => {
    expect(figuresUrl(null)).toBeNull();
  });
});

describe("figuresState", () => {
  const url = "/api/plans?model=a&stage=plan";
  const body = plansView();

  it("is none while there is no url", () => {
    expect(figuresState({ status: "loading" }, null)).toBe("none");
  });

  it("is loading while the read is out", () => {
    expect(figuresState({ status: "loading" }, url)).toBe("loading");
  });

  it("is loading while the answer held is for an earlier url", () => {
    expect(figuresState({ status: "ready", body, url: "/api/plans?model=b&stage=plan" }, url)).toBe(
      "loading",
    );
    expect(figuresState({ status: "error", url: "/api/plans?model=b&stage=plan" }, url)).toBe(
      "loading",
    );
  });

  it("is the read's own status once it is for this url", () => {
    expect(figuresState({ status: "ready", body, url }, url)).toBe("ready");
    expect(figuresState({ status: "error", url }, url)).toBe("error");
  });
});

describe("reloadFor", () => {
  const body = plansView();
  const ready: Read<PlansView> = { status: "ready", body, url: "/api/plans" };

  it("reads the plans again when they failed", () => {
    const calls: string[] = [];
    const catalogue = {
      status: "error" as const,
      url: "/api/plans",
      reload: () => calls.push("c"),
    };
    reloadFor(catalogue, { reload: () => calls.push("f") })();
    expect(calls).toEqual(["c"]);
  });

  it("reads the figures again when only they failed", () => {
    const calls: string[] = [];
    const catalogue = { ...ready, reload: () => calls.push("c") };
    reloadFor(catalogue, { reload: () => calls.push("f") })();
    expect(calls).toEqual(["f"]);
  });
});

describe("tokensLine", () => {
  it("prints each count grouped, in the drawn order", () => {
    expect(tokensLine({ fresh: 52000, cached: 2200, output: 1500 })).toBe(
      "52,000 fresh · 2,200 cached · 1,500 output",
    );
  });

  it("prints a zero count", () => {
    expect(tokensLine({ fresh: 13000, cached: 0, output: 1200 })).toBe(
      "13,000 fresh · 0 cached · 1,200 output",
    );
  });
});

describe("stageLabel", () => {
  it("capitalises the stage", () => {
    expect(stageLabel("review")).toBe("Review");
  });
});
