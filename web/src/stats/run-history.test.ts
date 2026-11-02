import { describe, expect, it } from "bun:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { RunHistoryView } from "./run-history";
import type { RunSuccess } from "./run-success";
import { RunSuccessCard, RunSuccessCardView } from "./run-success-card";
import type { Read } from "./use-read";

const KINDS = ["merged", "pr-open", "failed", "merged"] as const;

// runs is that many settled runs, oldest first, cycling through the kinds.
function runs(count: number, repo = "acme/widgets") {
  return Array.from({ length: count }, (_, i) => ({
    run: `run-${i}`,
    ticket: `T-${i}`,
    repo,
    kind: KINDS[i % KINDS.length],
    words: `words ${i}`,
  }));
}

type History = Parameters<typeof RunHistoryView>[0]["read"];

function ready(body: ReturnType<typeof runs>): History {
  return { status: "ready", body: { runs: body }, url: "/api/run-history" };
}

function draw(body: ReturnType<typeof runs>, project: string | null = null): string {
  return renderToStaticMarkup(createElement(RunHistoryView, { read: ready(body), project }));
}

function drawRead(read: History, project: string | null = null): string {
  return renderToStaticMarkup(createElement(RunHistoryView, { read, project }));
}

function strip(markup: string): string {
  return markup.match(/<ol [^>]*>.*?<\/ol>/)?.[0] ?? "";
}

describe("RunHistoryView runs", () => {
  it("has twenty tracks for twenty runs", () => {
    const markup = strip(draw(runs(20)));
    expect(markup).toContain("grid-template-columns:repeat(20, minmax(0, 16px))");
    expect(markup.match(/<li /g)).toHaveLength(20);
  });

  it("draws a single run as one bar", () => {
    expect(strip(draw(runs(1))).match(/<li /g)).toHaveLength(1);
  });

  it("lays the bars on a grid with a 5px gap and the strip's 26px height", () => {
    const markup = strip(draw(runs(2)));
    expect(markup).toMatch(/<ol [^>]*class="[^"]*\bgrid\b[^"]*\bgap-x-\[5px\]/);
    expect(markup).toMatch(/<li [^>]*class="[^"]*\bh-6\.5\b/);
  });

  it("keeps twenty tracks for three runs, so they take the first slots", () => {
    const markup = strip(draw(runs(3)));
    expect(markup).toContain("grid-template-columns:repeat(20, minmax(0, 16px))");
    expect(markup.match(/<li /g)).toHaveLength(3);
  });

  it("spreads the free width between the bars rather than stretching them", () => {
    expect(strip(draw(runs(20)))).toMatch(/<ol [^>]*class="[^"]*\bjustify-between\b/);
  });

  it("draws the runs in the order served, each read out with its ticket and words", () => {
    const words = [...draw(runs(4)).matchAll(/<span class="sr-only">([^<]*)<\/span>/g)].map(
      (m) => m[1],
    );
    expect(words).toEqual(["T-0: words 0", "T-1: words 1", "T-2: words 2", "T-3: words 3"]);
  });
});

describe("RunHistoryView around the runs", () => {
  it("holds the strip's height on every state line", () => {
    const lines = [
      drawRead({ status: "loading" }),
      drawRead({ status: "error", url: "/api/run-history" }),
      draw([]),
      draw(runs(3), "acme/gadgets"),
    ];
    for (const markup of lines) {
      expect(markup.match(/<p [^>]*class="[^"]*min-h-\[26px\]/g)).toHaveLength(1);
    }
  });

  it("captions the count and counts the merged runs", () => {
    const markup = draw(runs(4));
    expect(markup).toContain("Last 4 runs");
    expect(markup).toContain(">2 merged<");
  });

  it("narrows the strip and the caption to the project", () => {
    const mixed = [...runs(3), ...runs(2, "acme/gadgets")].map((run, i) => ({
      ...run,
      run: `run-${i}`,
    }));
    const markup = draw(mixed, "acme/gadgets");
    expect(markup).toContain("Last 2 runs, acme/gadgets");
    expect(strip(markup).match(/<li /g)).toHaveLength(2);
  });

  it("says no run has settled when the history is empty, with no strip", () => {
    const markup = draw([]);
    expect(markup).toContain("No run has settled yet.");
    expect(strip(markup)).toBe("");
  });

  it("names the legend's kinds in the loading state", () => {
    const loading = drawRead({ status: "loading" });
    expect(loading).toContain("Reading the run history…");
    expect(loading).toContain("merged");
    expect(loading).toContain("cancelled");
  });

  it("says the history could not be read, with no count or strip", () => {
    const markup = drawRead({ status: "error", url: "/api/run-history" });
    expect(markup).toContain("The run history could not be read.");
    expect(markup).toContain("Run history</h2>");
    expect(markup).not.toContain("merged</p>");
    expect(strip(markup)).toBe("");
  });

  it("captions one run as Last run", () => {
    expect(draw(runs(1))).toContain("Last run</h2>");
  });

  it("says no run for a project that none of the served runs belong to", () => {
    const markup = draw(runs(3), "acme/gadgets");
    expect(markup).toContain("No run for acme/gadgets in the last 3 runs.");
    expect(markup).toContain("Run history</h2>");
    expect(strip(markup)).toBe("");
  });

  it("counts a single served run in the singular", () => {
    expect(draw(runs(1), "acme/gadgets")).toContain("No run for acme/gadgets in the last 1 run.");
  });
});

const BELOW: RunSuccess = {
  recent: { passed: 55, runs: 64, rate: 55 / 64 },
  previous: { passed: 53, runs: 58, rate: 53 / 58 },
  failure: { class: "agent-timeout", count: 4 },
};

const CARD_READS = {
  ready: { status: "ready", body: BELOW, url: "/api/run-success" },
  loading: { status: "loading" },
  error: { status: "error", url: "/api/run-success" },
} as const satisfies Record<string, Read<RunSuccess>>;

const CARD_WORDS = {
  ready: "Below target",
  loading: "Reading the run success…",
  error: "The run success could not be read.",
} as const satisfies Record<keyof typeof CARD_READS, string>;

function inCard(card: Read<RunSuccess>, history: History, project: string | null = null): string {
  return renderToStaticMarkup(
    createElement(
      RunSuccessCardView,
      { read: card, projectChosen: project !== null },
      createElement(RunHistoryView, { read: history, project }),
    ),
  );
}

describe("the Run success card's runs section", () => {
  it("is the card's last section, under a hairline rule", () => {
    const markup = inCard(CARD_READS.ready, ready(runs(20)));
    expect(markup).toMatch(
      /<section aria-labelledby="run-history-title" class="[^"]*\bborder-t\b[^"]*\bborder-hair\b/,
    );
    expect(markup.indexOf('id="run-success-title"')).toBeLessThan(
      markup.indexOf('aria-labelledby="run-history-title"'),
    );
    expect(markup.endsWith("</ul></section></section>")).toBe(true);
  });

  it("is drawn by the card itself, with no block of its own beside it", () => {
    const markup = renderToStaticMarkup(createElement(RunSuccessCard, { project: null }));
    expect(markup).toContain('aria-labelledby="run-history-title"');
    expect(markup.endsWith("</ul></section></section>")).toBe(true);
  });

  it("names the card for Run success and the section by its caption", () => {
    const markup = inCard(CARD_READS.ready, ready(runs(4)));
    expect(markup).toContain('<section aria-labelledby="run-success-title"');
    expect(markup).toContain('<h2 id="run-success-title"');
    expect(markup).toContain('<h2 id="run-history-title"');
    expect(markup).toContain("Last 4 runs</h2>");
  });

  it.each(["ready", "loading", "error"] as const)(
    "draws its own runs while the card is %s",
    (state) => {
      const markup = inCard(CARD_READS[state], ready(runs(4)));
      expect(markup).toContain(CARD_WORDS[state]);
      expect(strip(markup).match(/<li /g)).toHaveLength(4);
    },
  );

  it.each([
    ["loading", { status: "loading" }, "Reading the run history…"],
    ["error", { status: "error", url: "/api/run-history" }, "The run history could not be read."],
    ["empty", ready([]), "No run has settled yet."],
  ] as const)("says its own %s while the card is read", (_, history, words) => {
    const markup = inCard(CARD_READS.ready, history);
    expect(markup).toContain("Below target");
    expect(markup).toContain(words);
  });

  it("keeps the card's pooled line and narrows only its own runs to a chosen project", () => {
    const mixed = [...runs(3), ...runs(2, "acme/gadgets")].map((run, i) => ({
      ...run,
      run: `run-${i}`,
    }));
    const markup = inCard(CARD_READS.ready, ready(mixed), "acme/gadgets");
    expect(markup).toContain("run success: all projects");
    expect(markup).toContain("Below target");
    expect(markup).toContain("Last 2 runs, acme/gadgets");
    expect(strip(markup).match(/<li /g)).toHaveLength(2);
  });
});
