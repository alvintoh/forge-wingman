import { describe, expect, it } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { CacheTable } from "./cache-table";
import type { RunCache, RunsState } from "./comparison";

const run = (over: Partial<RunCache> = {}): RunCache => ({
  run_id: "FRG-101",
  provider: "command-code",
  cached_share: 0.5,
  build_cache: "62% first, 58% phase",
  plan_cache: "41% first, 39% phase",
  build_first_share: 0.62,
  plan_first_share: 0.41,
  settled_at: "2026-10-29T13:42:00Z",
  ...over,
});

const ready = (runs: RunCache[]): RunsState => ({ status: "ready", runs });

// The component draws the table and the blocks from one row view; a case reads one tree at a time.
function render(state: RunsState, project: string | null = null) {
  const markup = renderToStaticMarkup(<CacheTable state={state} project={project} />);
  const table = /<table.*<\/table>/s.exec(markup)?.[0] ?? "";
  const blocks = /<ul.*<\/ul>/s.exec(markup)?.[0] ?? "";
  return { markup, table, blocks, text: markup.replaceAll("&#x27;", "'") };
}

const headings = (table: string) =>
  [...table.matchAll(/<th scope="col"[^>]*>([^<]*)<\/th>/g)].map((m) => m[1]);

const DEFINITION =
  "Build and plan are the share of that phase's opening call that came from cache, - where it was not recorded, none where the run had no such stage; whole run is every call in the run.";

describe("CacheTable", () => {
  it("draws the definition line exactly, with no all-projects note under a chosen project", () => {
    const all = render(ready([run()]));
    expect(all.text).toContain(`<p class="mt-2 text-cap leading-4 text-ink2">${DEFINITION}</p>`);
    expect(all.text).not.toContain("Runs from all projects.");
    const chosen = render(ready([run()]), "alvintoh/forge-wingman");
    expect(chosen.text).toContain(`${DEFINITION}</p>`);
    expect(chosen.text).not.toContain("all projects");
  });

  it("draws one header row of six columns, with none of the old pieces", () => {
    const { markup, table } = render(ready([run()]));
    expect(headings(table)).toEqual([
      "Run",
      "Provider",
      "Finished (AEST/AEDT)",
      "Build",
      "Plan",
      "Whole run",
    ]);
    expect(table.match(/<thead>.*<\/thead>/s)?.[0].match(/<tr/g)).toHaveLength(1);
    for (const old of ["First request", "Whole phase", "Show older runs", "older runs recorded"]) {
      expect(markup).not.toContain(old);
    }
  });

  it("pages five a page when no wide viewport is known, with one pager above the rows", () => {
    const runs = Array.from({ length: 37 }, (_, i) => run({ run_id: `FRG-${100 + i}` }));
    const { markup, table } = render(ready(runs));
    expect(markup.match(/Runs 1-5 of 37/g)).toHaveLength(1);
    expect(markup.indexOf("Runs 1-5 of 37")).toBeLessThan(markup.indexOf("<table"));
    expect(table.match(/<tbody>.*<\/tbody>/s)?.[0].match(/<tr/g)).toHaveLength(5);
  });

  it("draws a figure for a share and a dim word with a full-words name for the rest", () => {
    const rows = [
      run({ build_cache: "none", build_first_share: null }),
      run({ plan_cache: "none", plan_first_share: null }),
      run({ build_cache: "not recorded", build_first_share: null }),
    ];
    const { table } = render(ready(rows));
    expect(table).toContain(
      '<span role="img" aria-label="no build stage" class="text-ink2">none</span>',
    );
    expect(table).toContain(
      '<span role="img" aria-label="no plan stage" class="text-ink2">none</span>',
    );
    expect(table).toContain(
      '<span role="img" aria-label="not recorded" class="text-ink2">-</span>',
    );
    expect(table).toContain('<span class="font-mono text-ink">41%</span>');
  });

  it("sizes the columns from every run, not the page on screen", () => {
    const runs = Array.from({ length: 12 }, (_, i) => run({ run_id: `FRG-${100 + i}` }));
    runs[11] = run({ run_id: "FRG-1000", provider: "command-codx" });
    const { table } = render(ready(runs));
    expect(table).toContain("calc(8ch + 1rem)");
    expect(table).toContain("calc(12ch + 1rem)");
    expect(table).toContain("calc(18ch + 1rem)");
  });

  it("rounds a whole-run tie to the even percent", () => {
    expect(render(ready([run({ cached_share: 0.625 })])).table).toContain(">62%</td>");
  });

  it("words a run with no readable finish as not recorded, and a settled one with its year", () => {
    const { table } = render(ready([run({ settled_at: "" }), run()]));
    expect(table).toContain('<span class="whitespace-nowrap text-ink2">not recorded</span>');
    expect(table).toContain("30 Oct 2026, 00:42");
  });

  it("draws the stacked blocks as a list of labelled pairs from the same rows", () => {
    const { blocks } = render(ready([run({ plan_cache: "none", plan_first_share: null })]));
    expect(blocks.match(/<li/g)).toHaveLength(1);
    expect(blocks.match(/<dt/g)).toHaveLength(4);
    expect(blocks).toContain("Finished (AEST/AEDT)");
    expect(blocks).toContain('aria-label="no plan stage"');
  });

  it("says so when no run is recorded, drawing no table", () => {
    const { markup, table } = render(ready([]));
    expect(markup).toContain("No run is recorded.");
    expect(table).toBe("");
  });
});
