import { describe, expect, it } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { compareColumns } from "./compare";
import { CompareCard } from "./compare-card";
import { planOf, pricedModel, viewOf } from "./sample";
import { UNCONFIRMED } from "./view";
import type { Figure, Plan } from "./view";

const CHOICE = { model: "paid/zeta", stage: "plan" };

const FIGURE: Figure = {
  off_peak: 114,
  has_peak: true,
  peak: 57,
  shape: { model: "paid/zeta", fresh: 52000, cached: 2200, output: 1500 },
  mark: "exact",
};

const PAID = planOf("paid", {
  models: [{ ...pricedModel("paid/zeta"), peak: { multiplier: 2 } }],
  monthly_price_micros: 10_000_000,
  requests_per_dollar: FIGURE,
  five_hour: { limit_micros: 20_000_000, requests: { off_peak: 2297, has_peak: true, peak: 1148 } },
  features: ["One feature"],
  harnesses: [
    {
      harness: "omp",
      headless: true,
      structured_output: false,
      denies_planning_edits: UNCONFIRMED,
    },
  ],
  verdict: "allowed",
});
const FREE = planOf("free");

function markup(plans: Plan[] = [PAID, FREE]): string {
  return renderToStaticMarkup(<CompareCard columns={compareColumns(viewOf(plans), CHOICE)} />);
}

const TABLE_CARD = '<section aria-labelledby="compare-card-title"';

// The wide card's markup, and the phone cards' before it.
function parts(html: string): { phone: string; wide: string } {
  const at = html.indexOf(TABLE_CARD);
  return { phone: html.slice(0, at), wide: html.slice(at) };
}

function texts(html: string, tag: string, attrs = ""): string[] {
  return [...html.matchAll(new RegExp(`<${tag}${attrs}[^>]*>(.*?)</${tag}>`, "g"))].map((m) =>
    (m[1] ?? "").replace(/<[^>]*>/g, ""),
  );
}

describe("the plans side by side, wide", () => {
  it("is a card headed PLANS SIDE BY SIDE holding one table", () => {
    const { wide } = parts(markup());
    expect(wide).toMatch(/<h2 id="compare-card-title"[^>]*>plans side by side<\/h2>/);
    expect(wide.match(/<table/g)).toHaveLength(1);
  });

  it("heads a column per plan with its name and id", () => {
    const { wide } = parts(markup());
    expect(texts(wide, "th", ' scope="col"')).toEqual(["Paidpaid", "Freefree"]);
  });

  it("lists the sections and the rows under them in the drawn order", () => {
    const { wide } = parts(markup());
    expect(texts(wide, "th", ' scope="rowgroup"')).toEqual([
      "Cost",
      "Ceilings",
      "Models",
      "Features",
      "Harnesses",
      "Terms",
    ]);
    expect(texts(wide, "th", ' scope="row"')).toEqual([
      "Monthly price",
      "Requests per $1",
      "5-hour ceiling",
      "Weekly ceiling",
      "Monthly ceiling",
      "Strongest model",
      "Its per-model cap",
      "Features",
      "Harness",
      "Runs headless",
      "Structured output",
      "Planning run denies edits and shell",
      "Unattended use",
    ]);
  });

  it("sets each section's title as an uppercase caption", () => {
    expect(parts(markup()).wide).toMatch(/<th scope="rowgroup"[^>]*uppercase[^>]*>Cost<\/th>/);
  });

  it("holds a cell for every plan in every row, none empty", () => {
    const { wide } = parts(markup());
    const rows = wide.match(/<tr class="[^"]*">.*?<\/tr>/g) ?? [];
    expect(rows.length).toBeGreaterThanOrEqual(12);
    for (const row of rows) {
      for (const cell of row.match(/<td [^>]*>.*?<\/td>/g) ?? []) {
        expect(cell.replace(/<[^>]*>/g, "").trim()).not.toBe("");
      }
    }
    expect(wide.match(/<td /g)).toHaveLength(13 * 2);
  });

  it("draws every value of a paid plan's figures", () => {
    const { wide } = parts(markup());
    for (const text of [
      "$10 a month",
      "requests per $1",
      "at peak, price ×2",
      "52,000 fresh · 2,200 cached · 1,500 output",
      "$20",
      "2,297 requests off-peak",
      "1,148 requests at peak",
      "One feature",
      "allowed",
    ]) {
      expect(wide).toContain(text);
    }
    expect(wide).toContain(">114</span>");
    expect(wide).toContain(">57</span>");
  });

  it("says No figure, with its reason, for a plan without a rate card", () => {
    const { wide } = parts(markup());
    expect(wide).toContain("No figure");
    expect(wide).toContain("No rate card for zeta on this plan.");
  });

  it("draws nothing for no plan", () => {
    expect(renderToStaticMarkup(<CompareCard columns={[]} />)).toBe("");
  });
});

describe("the marks", () => {
  it("draws an exact figure's mark with a solid outline and a tick", () => {
    const html = markup([PAID]);
    const chip = html.match(/<span class="[^"]*rounded-full[^"]*">.*?<\/span>/)?.[0] ?? "";
    expect(chip).not.toContain("border-dashed");
    expect(chip).toContain("exact");
    expect(chip).toContain('<path d="M1.5 6.5l3 3l6 -7">');
  });

  it("draws an estimated figure's mark with a dashed outline and a wave", () => {
    const html = markup([
      planOf("paid", { requests_per_dollar: { ...FIGURE, mark: "estimated" } }),
    ]);
    const chip = html.match(/<span class="[^"]*rounded-full[^"]*">.*?<\/span>/)?.[0] ?? "";
    expect(chip).toContain("border-dashed");
    expect(chip).toContain("estimated");
    expect(chip).toContain("q1.5 -2 3 0t3 0");
    expect(chip).not.toContain("M1.5 6.5l3 3l6 -7");
  });

  it("draws a yes with a tick, a no with a cross and an unconfirmed value with a dashed ring", () => {
    const html = markup([
      planOf("a", {
        harnesses: [
          {
            harness: "omp",
            headless: true,
            structured_output: false,
            denies_planning_edits: UNCONFIRMED,
          },
        ],
      }),
    ]);
    const cellOf = (label: string) => {
      const row =
        html.match(new RegExp(`<th scope="row"[^>]*>${label}</th><td.*?</td>`))?.[0] ?? "";
      return row;
    };
    expect(cellOf("Runs headless")).toContain("M1.75 6.25l3 3l5.5 -6.5");
    expect(cellOf("Runs headless")).toContain(">yes<");
    expect(cellOf("Structured output")).toContain("M2.5 2.5l7 7M9.5 2.5l-7 7");
    expect(cellOf("Structured output")).toContain(">no<");
    expect(cellOf("Planning run denies edits and shell")).toContain('stroke-dasharray="2.5 2"');
    expect(cellOf("Planning run denies edits and shell")).toContain(">unconfirmed<");
  });
});

describe("the plans side by side, on a phone", () => {
  it("is one card per plan with no table, headed by its name and id", () => {
    const { phone } = parts(markup());
    expect(phone).not.toContain("<table");
    expect(phone.match(/<section aria-labelledby="compare-plan-/g)).toHaveLength(2);
    expect(texts(phone, "h3")).toEqual(["Paid", "Free"]);
  });

  it("carries the same sections and rows in each card as the table's column does", () => {
    const { phone, wide } = parts(markup());
    const cards = phone.split("<section ").slice(1);
    expect(cards).toHaveLength(2);
    for (const card of cards) {
      expect(texts(card, "h4")).toEqual(texts(wide, "th", ' scope="rowgroup"'));
      expect(texts(card, "dt")).toEqual(texts(wide, "th", ' scope="row"'));
      expect(card.match(/<dd/g)).toHaveLength(13);
    }
  });

  it("draws the same values as the table does for a plan", () => {
    const { phone } = parts(markup());
    const card = phone.split("<section ")[1] ?? "";
    for (const text of ["$10 a month", "at peak, price ×2", "2,297 requests off-peak", "allowed"]) {
      expect(card).toContain(text);
    }
  });

  it("is named by a heading hidden from sight, and the phone cards give way to the table from a wide column", () => {
    const html = markup();
    expect(html).toContain('<h2 class="sr-only @[47rem]:hidden">plans side by side</h2>');
    expect(html).toContain('<div class="flex min-w-0 flex-col gap-y-2 @[47rem]:hidden">');
    expect(parts(html).wide).toMatch(/^<section[^>]* class="[^"]*hidden[^"]*@\[47rem\]:block/);
  });
});

describe("a long or odd plan id", () => {
  const LONG = "a".repeat(70);

  it("wraps in the phone card's heading and id line, and in the table's header", () => {
    const html = markup([planOf(LONG)]);
    const { phone, wide } = parts(html);
    expect(phone.match(/<h3 [^>]*class="[^"]*min-w-0[^"]*break-words/)).not.toBeNull();
    expect(phone.match(/<p class="[^"]*min-w-0[^"]*break-words[^"]*">a{70}<\/p>/)).not.toBeNull();
    expect(wide).toMatch(/<span class="[^"]*break-words[^"]*">Aa{69}<\/span>/);
    expect(wide).toMatch(/<span class="[^"]*break-words[^"]*">a{70}<\/span>/);
  });

  it("names a phone card by an id with no space, tied to its heading, whatever the plan's id says", () => {
    const html = markup([planOf("has spaces"), planOf("has spaces")]);
    const { phone } = parts(html);
    const labelled = [...phone.matchAll(/<section aria-labelledby="([^"]*)"/g)].map((m) => m[1]);
    expect(labelled).toHaveLength(2);
    expect(labelled[0]).not.toBe(labelled[1]);
    for (const id of labelled) {
      expect(id).not.toMatch(/\s/);
      expect(phone).toContain(`<h3 id="${id}"`);
    }
  });
});

describe("the glyphs", () => {
  it("are hidden from assistive technology, the word beside each saying it", () => {
    const html = markup([
      planOf("a", {
        requests_per_dollar: FIGURE,
        harnesses: [
          {
            harness: "omp",
            headless: true,
            structured_output: false,
            denies_planning_edits: UNCONFIRMED,
          },
        ],
      }),
      planOf("b", { requests_per_dollar: { ...FIGURE, mark: "estimated" } }),
    ]);
    expect(html.match(/<svg /g)?.length).toBeGreaterThanOrEqual(8);
    expect(html.match(/<svg(?![^>]*aria-hidden="true")/g)).toBeNull();
  });
});

describe("a server string", () => {
  it("is drawn as text, never as markup", () => {
    const hostile = '<img src=x onerror="alert(1)"><script>alert(2)</script>';
    const html = markup([planOf(hostile, { features: [hostile], strongest: { model: hostile } })]);
    expect(html).not.toContain("<img");
    expect(html).not.toContain("<script");
    expect(html).toContain("&lt;img src=x onerror=&quot;alert(1)&quot;&gt;");
  });
});
