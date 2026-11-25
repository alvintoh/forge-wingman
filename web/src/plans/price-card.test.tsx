import { describe, expect, it } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { elementsOf, press } from "../element-tree";
import { PriceCard } from "./price-card";

const SHAPES = [
  { stage: "plan", tokens: { fresh: 52000, cached: 2200, output: 1500 } },
  { stage: "build", tokens: { fresh: 900, cached: 60000, output: 800 } },
  { stage: "review", tokens: { fresh: 13000, cached: 0, output: 1200 } },
];
const GROUPS = [
  {
    plan: "a",
    models: [
      { id: "a/claude", name: "claude" },
      { id: "a/deepseek", name: "deepseek" },
    ],
  },
  { plan: "b", models: [{ id: "b/gemini", name: "gemini" }] },
];

function noop() {}

type Overrides = Partial<Parameters<typeof PriceCard>[0]>;

function props(overrides: Overrides = {}): Parameters<typeof PriceCard>[0] {
  return {
    groups: GROUPS,
    shapes: SHAPES,
    choice: { model: "a/claude", stage: "plan" },
    busy: false,
    onModel: noop,
    onStage: noop,
    ...overrides,
  };
}

function markup(overrides: Overrides = {}): string {
  return renderToStaticMarkup(<PriceCard {...props(overrides)} />);
}

describe("the price-a-request card", () => {
  it("is headed PRICE A REQUEST and labels its select MODEL", () => {
    const html = markup();
    expect(html).toMatch(/<h2 id="price-card-title"[^>]*>price a request<\/h2>/);
    expect(html).toMatch(/<label for="price-model"[^>]*>model<\/label>/);
  });

  it("groups the select's models by plan, each shown by its bare name and valued by its qualified id", () => {
    const html = markup({ choice: { model: "a/deepseek", stage: "plan" } });
    expect(html.match(/<optgroup /g)).toHaveLength(2);
    expect(html).toMatch(/<optgroup label="a">[\s\S]*<\/optgroup><optgroup label="b">/);
    expect(html).toContain('<option value="a/deepseek" selected="">deepseek</option>');
    expect(html).toContain('<option value="a/claude">claude</option>');
    expect(html).toContain('<optgroup label="b"><option value="b/gemini">gemini</option>');
  });

  it("lists a radio row per shape, the chosen one checked, each printing its counts", () => {
    const html = markup();
    const radios = [...html.matchAll(/role="radio" aria-checked="(true|false)"/g)].map((m) => m[1]);
    expect(radios).toEqual(["true", "false", "false"]);
    for (const line of [
      "52,000 fresh · 2,200 cached · 1,500 output",
      "900 fresh · 60,000 cached · 800 output",
      "13,000 fresh · 0 cached · 1,200 output",
    ]) {
      expect(html).toContain(line);
    }
    expect(html).toMatch(/>Plan<\/span>[\s\S]*>Build<\/span>[\s\S]*>Review<\/span>/);
    expect(html).toContain('role="radiogroup" aria-labelledby="price-shape-label"');
  });

  it("names each radio by its stage and its counts", () => {
    expect(markup()).toContain('aria-labelledby="price-shape-build price-shape-build-counts"');
  });

  it("prints the legend under the card's controls", () => {
    const html = markup();
    expect(html).toContain(
      "Exact is the plan&#x27;s own rate card and the shape&#x27;s token counts.",
    );
    expect(html).toContain(
      "Estimated means an input is a published approximation. A limit that cannot be converted stays as the vendor published it.",
    );
  });

  it("marks itself busy while the figures are read", () => {
    expect(markup({ busy: true })).toContain('aria-busy="true"');
    expect(markup({ busy: false })).toContain('aria-busy="false"');
  });

  it("says so, with no radio group, when no stage has a shape", () => {
    const html = markup({ shapes: [], choice: null });
    expect(html).toContain("No stage has enough recorded runs to show a request.");
    expect(html).not.toContain("radiogroup");
    expect(html).toContain('<option value="a/claude" selected="">claude</option>');
  });

  it("gives the select the drawn 48px height and a 17px value", () => {
    const select = markup()
      .match(/<select[^>]*class="([^"]*)"/)?.[1]
      .split(" ");
    expect(select).toContain("h-12");
    expect(select).toContain("text-head");
  });

  it("gives every shape row at least the 44px target", () => {
    const rows = markup().match(/<div class="relative flex [^"]*"/g) ?? [];
    expect(rows).toHaveLength(3);
    for (const row of rows) {
      expect(row).toContain("min-h-15");
      expect(row).toContain("@[49rem]:min-h-11");
    }
  });
});

function radios(overrides: Overrides = {}) {
  return elementsOf(PriceCard(props(overrides))).filter(
    (element) => element.props.role === "radio",
  );
}

describe("the shape radios' keys", () => {
  it("make the chosen radio the group's one tab stop", () => {
    const stops = radios({ choice: { model: "a/claude", stage: "build" } }).map(
      (radio) => radio.props.tabIndex,
    );
    expect(stops).toEqual([-1, 0, -1]);
  });

  it("make the first radio the tab stop while none is chosen", () => {
    const stops = radios({ choice: null }).map((radio) => radio.props.tabIndex);
    expect(stops).toEqual([0, -1, -1]);
  });

  it("move the check with the arrow keys", () => {
    for (const radio of radios()) {
      expect(typeof radio.props.onKeyDown).toBe("function");
    }
  });
});

describe("choosing in the card", () => {
  it("lists the qualified id as each option's value and the bare name as its text", () => {
    const options = elementsOf(PriceCard(props())).filter((element) => element.type === "option");
    expect(options.map((option) => [option.props.value, option.props.children])).toEqual([
      ["a/claude", "claude"],
      ["a/deepseek", "deepseek"],
      ["b/gemini", "gemini"],
    ]);
  });

  it("hands the qualified id of the model chosen to onModel", () => {
    const chosen: string[] = [];
    const card = elementsOf(PriceCard(props({ onModel: (model) => chosen.push(model) })));
    const select = card.find((element) => element.type === "select");
    expect(select).toBeDefined();
    press(select!, "onChange", { target: { value: "b/gemini" } });
    expect(chosen).toEqual(["b/gemini"]);
  });

  it("hands the stage of the row chosen to onStage", () => {
    const chosen: string[] = [];
    const card = elementsOf(PriceCard(props({ onStage: (stage) => chosen.push(stage) })));
    const build = card.find(
      (element) =>
        element.props.role === "radio" &&
        String(element.props["aria-labelledby"]).startsWith("price-shape-build "),
    );
    expect(build).toBeDefined();
    press(build!, "onClick");
    expect(chosen).toEqual(["build"]);
  });
});
