import { describe, expect, it } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { compareColumns } from "./compare";
import { ModelPricesCard } from "./model-prices-card";
import { peakOf, planOf, pricedModel, viewOf } from "./sample";
import type { Plan } from "./view";

const ABOVE = {
  context_tokens: 100000,
  input_usd_per_mtok: 0.5,
  output_usd_per_mtok: 2.5,
  cache_read_usd_per_mtok: 0.05,
};

const GO = planOf("opencode-go", {
  models: [
    {
      ...pricedModel("opencode-go/deepseek", 0.15, 0.6, 0.003),
      peak: peakOf(
        2,
        [
          { from: 1, to: 4 },
          { from: 6, to: 10 },
        ],
        0.3,
        1.2,
        0.006,
      ),
    },
    pricedModel("opencode-go/muse", 0.1, 0.2, 0.002),
    { ...pricedModel("opencode-go/haiku", 0.1, 0.5, 0.01), above: ABOVE },
  ],
});
const FREE = planOf("gemini", { models: [pricedModel("gemini/lite", 0, 0, 0)] });

function columns(plans: Plan[]) {
  return compareColumns(viewOf(plans), null);
}

function markup(plans: Plan[] = [GO, FREE]): string {
  return renderToStaticMarkup(<ModelPricesCard columns={columns(plans)} />);
}

function texts(html: string, tag: string, attrs = ""): string[] {
  return [...html.matchAll(new RegExp(`<${tag}${attrs}[^>]*>(.*?)</${tag}>`, "g"))].map((m) =>
    (m[1] ?? "").replace(/<[^>]*>/g, ""),
  );
}

describe("the model prices card", () => {
  it("is headed MODEL PRICES, with its unit beside the heading", () => {
    const html = markup();
    expect(html).toMatch(/<h2 id="model-prices-title"[^>]*uppercase[^>]*>model prices<\/h2>/);
    expect(html).toContain("USD per 1M tokens");
    expect(html).toContain('<section aria-labelledby="model-prices-title"');
  });

  it("heads the rates Uncached input, Output and Cache read after the model and its rate", () => {
    expect(texts(markup(), "th", ' scope="col"')).toEqual([
      "Model",
      "Rate",
      "Uncached input",
      "Output",
      "Cache read",
    ]);
  });

  it("groups the models by plan, a plan's name left and its id right, in the server's order", () => {
    const html = markup();
    expect(texts(html, "th", ' scope="rowgroup"')).toEqual(["Opencode Go", "Gemini"]);
    expect(html.indexOf("opencode-go</td>")).toBeLessThan(html.indexOf("Gemini"));
    expect(html).toMatch(/colSpan="3"[^>]*text-right[^>]*>opencode-go<\/td>/);
  });

  it("names each model once, in mono, without its plan's prefix", () => {
    expect(texts(markup(), "th", ' scope="row"')).toEqual(["deepseek", "muse", "haiku", "lite"]);
  });

  it("gives a model with no peak and no tier an any hour row, three decimals right-aligned", () => {
    const html = markup([planOf("flat", { models: [pricedModel("flat/muse", 0.1, 0.2, 0.002)] })]);
    expect(texts(html, "td")).toContain("any hour");
    expect(texts(html, "td", ' class="[^"]*text-right')).toEqual(["0.100", "0.200", "0.002"]);
    expect(html).not.toContain("off-peak");
  });

  it("gives a model with a peak an off-peak row, then peak, price ×2 with its window", () => {
    const html = markup();
    const peak = html.indexOf("peak, price ×2");
    expect(html.indexOf("off-peak")).toBeGreaterThan(-1);
    expect(html.indexOf("off-peak")).toBeLessThan(peak);
    expect(html).toContain("weekdays 01:00–04:00 and 06:00–10:00 UTC");
    expect(html.slice(peak)).toContain("0.300");
    expect(html.slice(peak)).toContain("1.200");
    expect(html.slice(peak)).toContain("0.006");
  });

  it("gives a model with long-context rates a row for them, with the length separated by commas", () => {
    const html = markup();
    expect(html).toContain("over 100,000 tokens of context");
    expect(html.slice(html.indexOf("over 100,000"))).toContain("2.500");
  });

  it("prints a free plan's rates as 0.000", () => {
    const html = markup([FREE]);
    expect(texts(html, "td", ' class="[^"]*text-right')).toEqual(["0.000", "0.000", "0.000"]);
  });

  it("names the model on a row that continues it, for a reader of the cells", () => {
    expect(markup()).toContain(
      '<td class="py-3 pr-4 align-top"><span class="sr-only">deepseek</span></td>',
    );
  });

  it("draws a rule under every row but the last", () => {
    const rows = markup().match(/<tr class="[^"]*">/g) ?? [];
    expect(rows.filter((row) => row.includes("border-b"))).toHaveLength(rows.length - 1);
    expect(rows.at(-1)).toBe('<tr class="">');
  });

  it("leaves a plan with no models out, and the card out when none has any", () => {
    const html = markup([planOf("bare"), FREE]);
    expect(texts(html, "th", ' scope="rowgroup"')).toEqual(["Gemini"]);
    expect(markup([planOf("bare")])).toBe("");
    expect(markup([])).toBe("");
  });

  it("gives way to the plans' own cards on a narrow column, and is a table from a wide one", () => {
    const html = markup();
    expect(html).toContain('class="card-frame hidden pt-6 pb-4 @[47rem]:block"');
    expect(html).toContain('<div class="@container min-w-0">');
  });

  it("shows server strings as text", () => {
    const hostile = '<img src=x onerror="alert(1)"> & "quote"';
    const html = markup([
      planOf(hostile, {
        models: [{ ...pricedModel(`${hostile}/${hostile}`), peak: peakOf(2) }],
      }),
    ]);
    expect(html).not.toContain("<img");
    expect(html).toContain(
      "&lt;img src=x onerror=&quot;alert(1)&quot;&gt; &amp; &quot;quote&quot;",
    );
  });
});
