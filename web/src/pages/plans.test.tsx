import { describe, expect, it } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { elementsOf, press } from "../element-tree";
import { planOf, pricedModel, viewOf } from "../plans/sample";
import { isPlansView } from "../plans/view";
import type { Picked, PickChange, PlansView } from "../plans/view";
import type { Read } from "../stats/use-read";
import { usePlansPage } from "../plans/use-plans-page";
import type { PlansReader } from "../plans/use-plans-page";
import { PlansPageView } from "./plans";

// The free tier's model comes last, as the server sends it, and prices at $0.
const VIEW: PlansView = viewOf([
  planOf("paid", { models: [pricedModel("paid/zeta"), pricedModel("paid/alpha")] }),
  planOf("free", { models: [pricedModel("free/beta", 0, 0, 0)] }),
]);

const CATALOGUE = "/api/plans";
const FIGURES = "/api/plans?model=paid%2Fzeta&stage=plan";
const CHOSEN = "/api/plans?model=free%2Fbeta&stage=review";

type PageProps = Parameters<typeof PlansPageView>[0];

function noop() {}

function props(overrides: Partial<PageProps> = {}): PageProps {
  return {
    catalogue: { status: "ready", body: VIEW, url: CATALOGUE },
    figures: "ready",
    choice: { model: "paid/zeta", stage: "plan" },
    compared: VIEW,
    onPick: noop,
    onRetry: noop,
    ...overrides,
  };
}

function markup(overrides: Partial<PageProps> = {}): string {
  return renderToStaticMarkup(<PlansPageView {...props(overrides)} />);
}

function status(html: string): string {
  return html.match(/<p role="status" class="sr-only">([^<]*)<\/p>/)?.[1] ?? "no live region";
}

describe("the Plans page", () => {
  it("is headed PLANS above the price-a-request card", () => {
    const html = markup();
    expect(html).toMatch(/<h1[^>]*>plans<\/h1>/);
    expect(html.indexOf("<h1")).toBeLessThan(html.indexOf("price a request"));
  });

  it("opens on its choice, grouping the models by plan in the server's order", () => {
    const html = markup();
    expect(html).toContain('<option value="paid/zeta" selected="">zeta</option>');
    expect([...html.matchAll(/<optgroup label="([^"]*)"/g)].map((m) => m[1])).toEqual([
      "paid",
      "free",
    ]);
    expect([...html.matchAll(/<option value="([^"]*)"/g)].map((m) => m[1])).toEqual([
      "paid/zeta",
      "paid/alpha",
      "free/beta",
    ]);
    expect(
      [...html.matchAll(/role="radio" aria-checked="(true|false)"/g)].map((m) => m[1]),
    ).toEqual(["true", "false", "false"]);
  });

  it("does not list a stage with no shape", () => {
    const shapes = VIEW.shapes.map((s) => (s.stage === "build" ? { ...s, shape: null } : s));
    const html = markup({
      catalogue: { status: "ready", body: { ...VIEW, shapes }, url: CATALOGUE },
    });
    expect(html).toContain(">Plan</span>");
    expect(html).toContain(">Review</span>");
    expect(html).not.toContain(">Build</span>");
  });

  it("says no stage has a shape when none has", () => {
    const shapes = VIEW.shapes.map((s) => ({ ...s, shape: null }));
    const html = markup({
      catalogue: { status: "ready", body: { ...VIEW, shapes }, url: CATALOGUE },
      choice: null,
      figures: "none",
    });
    expect(html).toContain("No stage has enough recorded runs to show a request.");
    expect(html).not.toContain('role="radio"');
  });

  it("shows only the skeleton, with no card, while the plans are first read", () => {
    const html = markup({ catalogue: { status: "loading" }, choice: null, figures: "none" });
    expect(html).toContain("Reading the plans…");
    expect(html).toContain('aria-busy="true"');
    expect(html).not.toContain("price a request");
  });

  it("keeps the card and shows the skeleton under it while a new choice's figures are read", () => {
    const html = markup({ figures: "loading" });
    expect(html.indexOf("price a request")).toBeGreaterThan(-1);
    expect(html.indexOf("Reading the plans…", html.indexOf("price a request"))).toBeGreaterThan(-1);
    expect(html).toMatch(/<section aria-labelledby="price-card-title" aria-busy="true"/);
  });

  it("shows no skeleton once the figures are read", () => {
    const html = markup({ figures: "ready" });
    expect(html).not.toContain("Reading the plans…");
    expect(html).toMatch(/<section aria-labelledby="price-card-title" aria-busy="false"/);
  });

  it("shows only the error, with TRY AGAIN, when the plans could not be read", () => {
    const html = markup({
      catalogue: { status: "error", url: CATALOGUE },
      choice: null,
      figures: "none",
    });
    expect(html).toContain("The plans could not be read.");
    expect(html).toContain("TRY AGAIN");
    expect(html).not.toContain("price a request");
  });

  it("keeps the card above the error when only the figures could not be read", () => {
    const html = markup({ figures: "error" });
    expect(html.indexOf("price a request")).toBeGreaterThan(-1);
    expect(
      html.indexOf("The plans could not be read.", html.indexOf("price a request")),
    ).toBeGreaterThan(-1);
  });

  it("retries when TRY AGAIN is pressed", () => {
    let retries = 0;
    const page = elementsOf(
      PlansPageView(
        props({
          catalogue: { status: "error", url: CATALOGUE },
          choice: null,
          figures: "none",
          onRetry: () => {
            retries += 1;
          },
        }),
      ),
    );
    const button = page.find((element) => element.type === "button");
    expect(button).toBeDefined();
    press(button!, "onClick");
    expect(retries).toBe(1);
  });

  it("retries from the figures' error too", () => {
    let retries = 0;
    const page = elementsOf(
      PlansPageView(
        props({
          figures: "error",
          onRetry: () => {
            retries += 1;
          },
        }),
      ),
    );
    const button = page.find(
      (element) => element.type === "button" && element.props.role !== "radio",
    );
    press(button!, "onClick");
    expect(retries).toBe(1);
  });

  it("picks a model without a stage, and a stage without a model", () => {
    const picks: PickChange[] = [];
    const page = elementsOf(PlansPageView(props({ onPick: (change) => picks.push(change) })));
    press(
      page.find((element) => element.type === "select")!,
      "onChange",
      { target: { value: "paid/alpha" } },
    );
    press(
      page.find(
        (element) =>
          element.props.role === "radio" &&
          String(element.props["aria-labelledby"]).startsWith("price-shape-review "),
      )!,
      "onClick",
    );
    expect(picks).toEqual([{ model: "paid/alpha" }, { stage: "review" }]);
  });
});

describe("the Plans page's plans side by side", () => {
  it("sits under the price-a-request card, one column per plan", () => {
    const html = markup();
    expect(html.indexOf("price a request")).toBeLessThan(html.indexOf("plans side by side"));
    expect(html.match(/<th scope="col"/g)).toHaveLength(2);
    expect(html).toContain(">Paid</span>");
    expect(html).toContain(">Free</span>");
  });

  it("is drawn from the answer it is given, and from none while the figures are read", () => {
    const answer = viewOf([planOf("alone", { monthly_price_micros: 10_000_000 })]);
    expect(markup({ compared: answer })).toContain("$10 a month");
    const loading = markup({ figures: "loading", compared: null });
    expect(loading).not.toContain("plans side by side");
    expect(loading).toContain("Reading the plans…");
  });

  it("leaves the card out, under the price card's error, when the figures could not be read", () => {
    const html = markup({ figures: "error", compared: null });
    expect(html).not.toContain("plans side by side");
    expect(html).toContain("The plans could not be read.");
  });

  it("is not drawn while the plans are first read, or when they could not be", () => {
    const loading = markup({ catalogue: { status: "loading" }, choice: null, compared: null });
    const failed = markup({
      catalogue: { status: "error", url: CATALOGUE },
      choice: null,
      compared: null,
    });
    expect(loading).not.toContain("plans side by side");
    expect(failed).not.toContain("plans side by side");
  });

  it("puts the skeleton for new figures after the card it covers, not instead of the price card", () => {
    const html = markup({ figures: "loading", compared: null });
    expect(html.indexOf("price a request")).toBeLessThan(
      html.indexOf("Reading the plans…", html.indexOf("price a request")),
    );
  });
});

describe("the Plans page's live region", () => {
  it("is in the page, empty, once the plans and the figures are read", () => {
    expect(status(markup())).toBe("");
  });

  it("holds the reading sentence while the plans are first read", () => {
    expect(status(markup({ catalogue: { status: "loading" }, choice: null }))).toBe(
      "Reading the plans…",
    );
  });

  it("holds it again while a new choice's figures are read", () => {
    expect(status(markup({ figures: "loading" }))).toBe("Reading the plans…");
  });

  it("holds the failure sentence when either read failed", () => {
    expect(status(markup({ figures: "error" }))).toBe("The plans could not be read.");
    expect(status(markup({ catalogue: { status: "error", url: CATALOGUE }, choice: null }))).toBe(
      "The plans could not be read.",
    );
  });

  it("comes before the cards, and the cards' own sentence is hidden from readers", () => {
    const html = markup({ figures: "error" });
    expect(html.indexOf('role="status"')).toBeLessThan(html.indexOf("price a request"));
    expect(html).toContain('<p aria-hidden="true" class="text-head leading-6 text-ink">');
    expect(markup({ figures: "loading" })).toContain(
      '<p aria-hidden="true" class="text-body leading-5 text-ink2">Reading the plans…</p>',
    );
  });
});

type Reads = { url: string | null; guard: unknown; token: string }[];

// A reader that records each read the page makes and answers it from `answer`.
function reader(answer: (url: string | null) => Read<PlansView>) {
  const reads: Reads = [];
  const reloads: (string | null)[] = [];
  const read: PlansReader = (url, guard, token) => {
    reads.push({ url, guard, token });
    return {
      ...answer(url),
      reload: () => {
        reloads.push(url);
      },
    };
  };
  return { read, reads, reloads };
}

// The props usePlansPage hands the page, from one render.
function pageOf(read: PlansReader, initial: Picked = {}): PageProps {
  const pages: PageProps[] = [];
  function Probe() {
    pages.push(usePlansPage(read, initial));
    return null;
  }
  renderToStaticMarkup(<Probe />);
  const [page] = pages;
  if (page === undefined) {
    throw new Error("the page did not render");
  }
  return page;
}

const READY: Read<PlansView> = { status: "ready", body: VIEW, url: CATALOGUE };

describe("usePlansPage", () => {
  it("reads the plans first, checked by the plans guard", () => {
    const { read, reads } = reader(() => ({ status: "loading" }));
    pageOf(read);
    expect(reads[0]?.url).toBe(CATALOGUE);
    expect(reads[0]?.guard).toBe(isPlansView);
  });

  it("reads no figures while the plans are not in", () => {
    const { read, reads } = reader(() => ({ status: "loading" }));
    const page = pageOf(read);
    expect(reads[1]?.url).toBeNull();
    expect(page.figures).toBe("none");
    expect(page.choice).toBeNull();
  });

  it("reads the figures at the first model some plan prices above $0 and the first stage", () => {
    const { read, reads } = reader((url) => (url === CATALOGUE ? READY : { status: "loading" }));
    const page = pageOf(read);
    expect(reads[1]?.url).toBe(FIGURES);
    expect(reads[1]?.guard).toBe(isPlansView);
    expect(page.choice).toEqual({ model: "paid/zeta", stage: "plan" });
    expect(page.figures).toBe("loading");
  });

  it("reads the figures at the choice the owner made, not the first", () => {
    const { read, reads } = reader((url) => (url === CATALOGUE ? READY : { status: "loading" }));
    const page = pageOf(read, { model: "free/beta", stage: "review" });
    expect(reads[1]?.url).toBe(CHOSEN);
    expect(page.choice).toEqual({ model: "free/beta", stage: "review" });
  });

  it("reads no figures while no stage has a shape", () => {
    const shapes = VIEW.shapes.map((s) => ({ ...s, shape: null }));
    const { read, reads } = reader(() => ({
      status: "ready",
      body: { ...VIEW, shapes },
      url: CATALOGUE,
    }));
    const page = pageOf(read);
    expect(reads[1]?.url).toBeNull();
    expect(page.figures).toBe("none");
  });

  it("holds the figures as loading while the answer in hand is for another choice", () => {
    const stale: Read<PlansView> = {
      status: "ready",
      body: VIEW,
      url: "/api/plans?model=x&stage=plan",
    };
    const { read } = reader((url) => (url === CATALOGUE ? READY : stale));
    expect(pageOf(read).figures).toBe("loading");
  });

  it("holds the figures as ready, or as failed, once the read is for this choice", () => {
    const answered = reader((url) => (url === CATALOGUE ? READY : { ...READY, url: FIGURES }));
    expect(pageOf(answered.read).figures).toBe("ready");
    const failed = reader((url) => (url === CATALOGUE ? READY : { status: "error", url: FIGURES }));
    expect(pageOf(failed.read).figures).toBe("error");
  });

  it("compares the figures' own answer once it is in, the catalogue's while nothing is priced", () => {
    const figuresBody = viewOf([planOf("priced")]);
    const priced = reader((url) =>
      url === CATALOGUE ? READY : { status: "ready", body: figuresBody, url: FIGURES },
    );
    expect(pageOf(priced.read).compared).toBe(figuresBody);
    const noShapes = { ...VIEW, shapes: VIEW.shapes.map((s) => ({ ...s, shape: null })) };
    const none = reader(() => ({ status: "ready", body: noShapes, url: CATALOGUE }));
    expect(pageOf(none.read).compared).toBe(noShapes);
  });

  it("compares nothing while the figures are read or could not be", () => {
    const loading = reader((url) => (url === CATALOGUE ? READY : { status: "loading" }));
    expect(pageOf(loading.read).compared).toBeNull();
    const failed = reader((url) => (url === CATALOGUE ? READY : { status: "error", url: FIGURES }));
    expect(pageOf(failed.read).compared).toBeNull();
  });

  it("retries the plans when they failed, and the figures when only they did", () => {
    const plans = reader((url) =>
      url === CATALOGUE ? { status: "error", url: CATALOGUE } : { status: "loading" },
    );
    pageOf(plans.read).onRetry();
    expect(plans.reloads).toEqual([CATALOGUE]);
    const figures = reader((url) =>
      url === CATALOGUE ? READY : { status: "error", url: FIGURES },
    );
    pageOf(figures.read).onRetry();
    expect(figures.reloads).toEqual([FIGURES]);
    const loading = reader((url) => (url === CATALOGUE ? READY : { status: "loading" }));
    pageOf(loading.read).onRetry();
    expect(loading.reloads).toEqual([FIGURES]);
  });
});
