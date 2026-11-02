import { describe, expect, it } from "bun:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { RunHistoryView } from "./run-history";

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

function draw(body: ReturnType<typeof runs>, project: string | null = null): string {
  const read = { status: "ready", body: { runs: body }, url: "/api/run-history" } as const;
  return renderToStaticMarkup(createElement(RunHistoryView, { read, project }));
}

function strip(markup: string): string {
  return markup.match(/<ol [^>]*>.*?<\/ol>/)?.[0] ?? "";
}

describe("RunHistoryView strip", () => {
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

describe("RunHistoryView around the strip", () => {
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
    const loading = renderToStaticMarkup(
      createElement(RunHistoryView, { read: { status: "loading" }, project: null }),
    );
    expect(loading).toContain("Reading the run history");
    expect(loading).toContain("merged");
    expect(loading).toContain("cancelled");
  });
});
