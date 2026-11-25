import { describe, expect, it } from "bun:test";
import { readFileSync } from "node:fs";
import { compareColumns, dollars, monthlyPrice, planName } from "./compare";
import type { Piece, Row } from "./compare";
import { peakOf, planOf, pricedModel, viewOf } from "./sample";
import { isPlansView, UNCONFIRMED } from "./view";
import type { Choice, Figure, Plan, PlansView } from "./view";

const fixture: unknown = JSON.parse(
  readFileSync(new URL("../../fixtures/plans.json", import.meta.url), "utf8"),
);

const CHOICE: Choice = { model: "paid/zeta", stage: "plan" };
const TOKENS = { fresh: 52000, cached: 2200, output: 1500 };

function figure(overrides: Partial<Figure> = {}): Figure {
  return {
    off_peak: 114,
    has_peak: false,
    peak: 0,
    shape: { model: "paid/zeta", ...TOKENS },
    mark: "exact",
    ...overrides,
  };
}

// The pieces of the row a plan's column holds under `label`.
function cell(plan: Plan, label: string, choice: Choice | null = CHOICE): Piece[] {
  const [column] = compareColumns(viewOf([plan]), choice);
  const row = column?.sections.flatMap((section) => section.rows).find((r) => r.label === label);
  if (row === undefined) {
    throw new Error(`no row ${label}`);
  }
  return row.pieces;
}

const unconfirmed: Piece = { kind: "glyph", glyph: "unconfirmed", word: UNCONFIRMED };

describe("dollars", () => {
  it("writes whole dollars bare and the rest to the cent", () => {
    expect(dollars(0)).toBe("$0");
    expect(dollars(20_000_000)).toBe("$20");
    expect(dollars(12_500_000)).toBe("$12.50");
    expect(dollars(100_000)).toBe("$0.10");
  });

  it("keeps every place it holds, up to six, without a float's error", () => {
    expect(dollars(1)).toBe("$0.000001");
    expect(dollars(15_005_000)).toBe("$15.005");
    expect(dollars(299_999_999_999)).toBe("$299,999.999999");
    expect(dollars(Number.MAX_SAFE_INTEGER)).toBe("$9,007,199,254.740991");
  });

  it("separates thousands", () => {
    expect(dollars(1_234_000_000)).toBe("$1,234");
  });
});

describe("monthlyPrice", () => {
  it("words a price as the drawing does, and $0 as the free tier", () => {
    expect(monthlyPrice(10_000_000)).toBe("$10 a month");
    expect(monthlyPrice(12_500_000)).toBe("$12.50 a month");
    expect(monthlyPrice(0)).toBe("$0, free tier");
  });
});

describe("planName", () => {
  it("is the id as capitalised words", () => {
    expect(planName("opencode-go")).toBe("Opencode Go");
    expect(planName("gemini")).toBe("Gemini");
  });
});

describe("compareColumns", () => {
  it("is one column per plan, in the server's order, with the plan's name and id", () => {
    const columns = compareColumns(viewOf([planOf("opencode-go"), planOf("gemini")]), null);
    expect(columns.map((c) => [c.name, c.id])).toEqual([
      ["Opencode Go", "opencode-go"],
      ["Gemini", "gemini"],
    ]);
  });

  it("carries each plan's models' prices, none for a plan with no models", () => {
    const [paid, bare] = compareColumns(
      viewOf([planOf("paid", { models: [pricedModel("paid/zeta")] }), planOf("bare")]),
      null,
    );
    expect(paid?.prices.map((model) => model.name)).toEqual(["zeta"]);
    expect(bare?.prices).toEqual([]);
  });

  it("is no column for no plan", () => {
    expect(compareColumns(viewOf([]), null)).toEqual([]);
  });

  it("holds the six sections and thirteen rows in the drawn order, in every column", () => {
    for (const column of compareColumns(viewOf([planOf("a"), planOf("b")]), CHOICE)) {
      expect(column.sections.map((s) => [s.title, s.rows.map((r) => r.label)])).toEqual([
        ["Cost", ["Monthly price", "Requests per $1"]],
        ["Ceilings", ["5-hour ceiling", "Weekly ceiling", "Monthly ceiling"]],
        ["Models", ["Strongest model", "Its per-model cap"]],
        ["Features", ["Features"]],
        [
          "Harnesses",
          ["Harness", "Runs headless", "Structured output", "Planning run denies edits and shell"],
        ],
        ["Terms", ["Unattended use"]],
      ]);
    }
  });

  it("leaves no cell empty, for a plan with nothing confirmed or the endpoint's own plans", () => {
    const view: unknown = fixture;
    if (!isPlansView(view)) {
      throw new Error("the fixture is not a plans view");
    }
    const views: PlansView[] = [viewOf([planOf("bare")]), view];
    for (const one of views) {
      for (const choice of [null, CHOICE]) {
        const rows: Row[] = compareColumns(one, choice).flatMap((c) =>
          c.sections.flatMap((s) => s.rows),
        );
        expect(rows.length).toBeGreaterThan(0);
        for (const row of rows) {
          expect(row.pieces.length).toBeGreaterThan(0);
        }
      }
    }
  });

  it("shows a plan with nothing confirmed as unconfirmed in every row it can, never blank", () => {
    const [column] = compareColumns(viewOf([planOf("bare")]), null);
    const rows = column?.sections.flatMap((s) => s.rows) ?? [];
    expect(rows.filter((r) => r.label !== "Requests per $1").map((r) => r.pieces)).toEqual(
      Array(12).fill([unconfirmed]),
    );
  });
});

describe("the monthly price cell", () => {
  it("words a confirmed price, and an unconfirmed one as unconfirmed", () => {
    expect(cell(planOf("a", { monthly_price_micros: 10_000_000 }), "Monthly price")).toEqual([
      { kind: "text", text: "$10 a month" },
    ]);
    expect(cell(planOf("a", { monthly_price_micros: 0 }), "Monthly price")).toEqual([
      { kind: "text", text: "$0, free tier" },
    ]);
    expect(cell(planOf("a"), "Monthly price")).toEqual([unconfirmed]);
  });
});

describe("the requests per $1 cell", () => {
  it("is the figure with its unit, its mark and the shape's counts when the plan has one", () => {
    const plan = planOf("paid", { requests_per_dollar: figure() });
    expect(cell(plan, "Requests per $1")).toEqual([
      { kind: "figure", value: "114" },
      { kind: "mark", mark: "exact" },
      { kind: "shape", text: "52,000 fresh · 2,200 cached · 1,500 output" },
    ]);
  });

  it("puts a thousands separator in a large figure", () => {
    const plan = planOf("paid", { requests_per_dollar: figure({ off_peak: 12_345 }) });
    expect(cell(plan, "Requests per $1")[0]).toEqual({ kind: "figure", value: "12,345" });
  });

  it("marks an estimated figure estimated", () => {
    const plan = planOf("paid", { requests_per_dollar: figure({ mark: "estimated" }) });
    expect(cell(plan, "Requests per $1")).toContainEqual({ kind: "mark", mark: "estimated" });
  });

  it("adds the peak figure, in the multiplier the model's own price carries, when the model has a peak", () => {
    const peaked = { ...pricedModel("paid/zeta"), peak: peakOf(2) };
    const plan = planOf("paid", {
      models: [peaked],
      requests_per_dollar: figure({ has_peak: true, peak: 57 }),
    });
    expect(cell(plan, "Requests per $1")[1]).toEqual({
      kind: "peak",
      value: "57",
      words: "at peak, price ×2",
    });
  });

  it("reads the multiplier off the figure's own model, not the plan's first", () => {
    const plan = planOf("paid", {
      models: [pricedModel("paid/alpha"), { ...pricedModel("paid/zeta"), peak: peakOf(2) }],
      requests_per_dollar: figure({ has_peak: true, peak: 57 }),
    });
    expect(cell(plan, "Requests per $1")[1]).toEqual({
      kind: "peak",
      value: "57",
      words: "at peak, price ×2",
    });
  });

  it("says the multiplier the price carries and never a drawn 2", () => {
    const peaked = { ...pricedModel("paid/zeta"), peak: peakOf(1.5) };
    const plan = planOf("paid", {
      models: [peaked],
      requests_per_dollar: figure({ has_peak: true, peak: 76 }),
    });
    expect(cell(plan, "Requests per $1")[1]).toEqual({
      kind: "peak",
      value: "76",
      words: "at peak, price ×1.5",
    });
  });

  it("states no multiplier when the plan's price for the model does not carry one", () => {
    const plan = planOf("paid", {
      models: [pricedModel("paid/zeta")],
      requests_per_dollar: figure({ has_peak: true, peak: 57 }),
    });
    expect(cell(plan, "Requests per $1")[1]).toEqual({
      kind: "peak",
      value: "57",
      words: "at peak",
    });
  });

  it("adds no peak line for a model with no peak window", () => {
    const plan = planOf("paid", { requests_per_dollar: figure({ has_peak: false, peak: 0 }) });
    expect(cell(plan, "Requests per $1").map((p) => p.kind)).toEqual(["figure", "mark", "shape"]);
  });

  it("is No figure, saying the model has no rate card on the plan, when it has none", () => {
    const plan = planOf("other", { models: [pricedModel("other/different")] });
    expect(cell(plan, "Requests per $1")).toEqual([
      { kind: "label", text: "No figure" },
      { kind: "note", text: "No rate card for zeta on this plan." },
    ]);
  });

  it("is No figure alone while no model is chosen", () => {
    expect(cell(planOf("a"), "Requests per $1", null)).toEqual([
      { kind: "label", text: "No figure" },
    ]);
  });

  it("is Free when the plan's rate card prices every token at $0", () => {
    const plan = planOf("free", { models: [pricedModel("paid/zeta", 0, 0, 0)] });
    expect(cell(plan, "Requests per $1")).toEqual([
      { kind: "headline", text: "Free" },
      { kind: "note", text: "Its rate card prices every token at $0." },
    ]);
  });

  it("is not Free when any one rate is above $0", () => {
    for (const rates of [
      [0.1, 0, 0],
      [0, 0.1, 0],
      [0, 0, 0.1],
    ] as const) {
      const plan = planOf("p", { models: [pricedModel("paid/zeta", ...rates)] });
      expect(cell(plan, "Requests per $1")).toEqual([{ kind: "label", text: "No figure" }]);
    }
  });
});

describe("the ceiling cells", () => {
  it("shows the dollar limit with its requests off-peak and at peak", () => {
    const plan = planOf("a", {
      five_hour: {
        limit_micros: 20_000_000,
        requests: { off_peak: 2297, has_peak: true, peak: 1148 },
      },
    });
    expect(cell(plan, "5-hour ceiling")).toEqual([
      { kind: "limit", text: "$20" },
      { kind: "requests", text: "2,297 requests off-peak" },
      { kind: "requests", text: "1,148 requests at peak" },
    ]);
  });

  it("shows no peak line for a ceiling without a peak, and no mark on either count", () => {
    const plan = planOf("a", {
      weekly: { limit_micros: 50_000_000, requests: { off_peak: 5742, has_peak: false, peak: 0 } },
    });
    expect(cell(plan, "Weekly ceiling")).toEqual([
      { kind: "limit", text: "$50" },
      { kind: "requests", text: "5,742 requests off-peak" },
    ]);
  });

  it("shows the limit alone, as published, when it cannot be converted", () => {
    const plan = planOf("a", { monthly: { limit_micros: 60_000_000, requests: null } });
    expect(cell(plan, "Monthly ceiling")).toEqual([{ kind: "limit", text: "$60" }]);
  });

  it("fills each ceiling from its own field", () => {
    const plan = planOf("a", {
      five_hour: { limit_micros: 1_000_000, requests: null },
      monthly: { limit_micros: 3_000_000, requests: null },
    });
    expect(cell(plan, "5-hour ceiling")).toEqual([{ kind: "limit", text: "$1" }]);
    expect(cell(plan, "Weekly ceiling")).toEqual([unconfirmed]);
    expect(cell(plan, "Monthly ceiling")).toEqual([{ kind: "limit", text: "$3" }]);
  });
});

describe("the models cells", () => {
  it("names the strongest model without its plan's prefix, and its cap by the month", () => {
    const plan = planOf("opencode-go", {
      strongest: { model: "opencode-go/muse" },
      strongest_cap_micros: 90_000_000,
    });
    expect(cell(plan, "Strongest model")).toEqual([{ kind: "text", text: "muse" }]);
    expect(cell(plan, "Its per-model cap")).toEqual([{ kind: "text", text: "$90 a month" }]);
  });

  it("keeps a model of another plan's prefix as it is", () => {
    const plan = planOf("a", { strongest: { model: "b/muse" } });
    expect(cell(plan, "Strongest model")).toEqual([{ kind: "text", text: "b/muse" }]);
  });
});

describe("the features cell", () => {
  it("lists the configuration's features", () => {
    const plan = planOf("a", { features: ["One", "Two"] });
    expect(cell(plan, "Features")).toEqual([{ kind: "list", items: ["One", "Two"] }]);
  });

  it("shows the configuration's features only, never the feed's perks the endpoint also serves", () => {
    const plan = { ...planOf("a", { features: ["One"] }), feed_perks: ["Perk"] };
    expect(cell(plan, "Features")).toEqual([{ kind: "list", items: ["One"] }]);
  });

  it("is unconfirmed when there is nothing to list", () => {
    expect(cell(planOf("a"), "Features")).toEqual([unconfirmed]);
  });
});

describe("the harness cells", () => {
  const omp = {
    harness: "omp",
    headless: true,
    structured_output: false,
    denies_planning_edits: UNCONFIRMED,
  } as const;

  it("is a tick for yes, a cross for no and a dashed ring for unconfirmed, each with its word", () => {
    const plan = planOf("a", { harnesses: [omp] });
    expect(cell(plan, "Runs headless")).toEqual([{ kind: "glyph", glyph: "yes", word: "yes" }]);
    expect(cell(plan, "Structured output")).toEqual([{ kind: "glyph", glyph: "no", word: "no" }]);
    expect(cell(plan, "Planning run denies edits and shell")).toEqual([unconfirmed]);
    expect(cell(plan, "Harness")).toEqual([{ kind: "text", text: "omp" }]);
  });

  it("names the harness before each word when a plan has several", () => {
    const other = { ...omp, harness: "cli", headless: false, denies_planning_edits: true };
    const plan = planOf("a", { harnesses: [omp, other] });
    expect(cell(plan, "Harness")).toEqual([
      { kind: "text", text: "omp" },
      { kind: "text", text: "cli" },
    ]);
    expect(cell(plan, "Runs headless")).toEqual([
      { kind: "glyph", glyph: "yes", word: "omp: yes" },
      { kind: "glyph", glyph: "no", word: "cli: no" },
    ]);
    expect(cell(plan, "Planning run denies edits and shell")).toEqual([
      { kind: "glyph", glyph: "unconfirmed", word: "omp: unconfirmed" },
      { kind: "glyph", glyph: "yes", word: "cli: yes" },
    ]);
  });

  it("is unconfirmed in every harness row for a plan with no harness", () => {
    const plan = planOf("a", { harnesses: [] });
    for (const label of [
      "Harness",
      "Runs headless",
      "Structured output",
      "Planning run denies edits and shell",
    ]) {
      expect(cell(plan, label)).toEqual([unconfirmed]);
    }
  });
});

describe("the unattended use cell", () => {
  it("is a tick for allowed, a cross for restricted and unconfirmed otherwise", () => {
    expect(cell(planOf("a", { verdict: "allowed" }), "Unattended use")).toEqual([
      { kind: "glyph", glyph: "yes", word: "allowed" },
    ]);
    expect(cell(planOf("a", { verdict: "restricted" }), "Unattended use")).toEqual([
      { kind: "glyph", glyph: "no", word: "restricted" },
    ]);
    expect(cell(planOf("a"), "Unattended use")).toEqual([unconfirmed]);
  });
});

describe("the endpoint's own answer", () => {
  it("reads as a paid plan with figures beside a free plan with unconfirmed fields", () => {
    const view: unknown = fixture;
    if (!isPlansView(view)) {
      throw new Error("the fixture is not a plans view");
    }
    const [paid, free] = compareColumns(view, {
      model: "opencode-go/claude-haiku-5-5",
      stage: "plan",
    });
    const rows = (column: typeof paid) => column?.sections.flatMap((s) => s.rows) ?? [];
    const row = (column: typeof paid, label: string) =>
      rows(column).find((r) => r.label === label)?.pieces;
    expect(row(paid, "Monthly price")).toEqual([{ kind: "text", text: "$15 a month" }]);
    expect(row(paid, "Requests per $1")?.[0]).toEqual({ kind: "figure", value: "167" });
    expect(row(paid, "5-hour ceiling")).toContainEqual({ kind: "limit", text: "$20" });
    expect(row(free, "Monthly price")).toEqual([unconfirmed]);
    expect(row(free, "Requests per $1")).toEqual([
      { kind: "label", text: "No figure" },
      { kind: "note", text: "No rate card for claude-haiku-5-5 on this plan." },
    ]);
  });
});
